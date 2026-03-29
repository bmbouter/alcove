// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package gate implements the Gate authorization proxy for Alcove.
// Gate runs as a sidecar in each Skiff pod, enforcing scope-based access
// control and credential injection for all outbound requests.
package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bmbouter/alcove/internal"
)

// ParseScope deserializes a JSON-encoded Scope.
func ParseScope(data string) (internal.Scope, error) {
	var scope internal.Scope
	if err := json.Unmarshal([]byte(data), &scope); err != nil {
		return scope, fmt.Errorf("parsing scope: %w", err)
	}
	return scope, nil
}

// AccessResult describes the outcome of a scope check.
type AccessResult struct {
	Allowed   bool
	Service   string
	Operation string
	Reason    string
}

// CheckAccess evaluates whether a request (method + URL) is permitted by the scope.
func CheckAccess(method, rawURL string, scope internal.Scope) AccessResult {
	u, err := url.Parse(rawURL)
	if err != nil {
		return AccessResult{Allowed: false, Reason: "invalid URL"}
	}

	host := u.Hostname()

	// GitHub API
	if host == "api.github.com" {
		return checkGitHub(method, u.Path, scope)
	}

	// GitLab API (any gitlab.* host)
	if strings.Contains(host, "gitlab") {
		return checkGitLab(method, u.Path, scope)
	}

	// Atlassian / Jira
	if strings.HasSuffix(host, ".atlassian.net") {
		return checkAtlassian(method, u.Path, scope)
	}

	return AccessResult{Allowed: false, Service: "unknown", Reason: fmt.Sprintf("host %q is not a recognized service", host)}
}

// checkGitHub maps GitHub API URLs to operations and checks them against scope.
func checkGitHub(method, path string, scope internal.Scope) AccessResult {
	svcScope, ok := scope.Services["github"]
	if !ok {
		return AccessResult{Allowed: false, Service: "github", Reason: "github not in scope"}
	}

	// Parse /repos/{owner}/{repo}/... paths
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	if len(parts) >= 3 && parts[0] == "repos" {
		repo := parts[1] + "/" + parts[2]
		if !repoAllowed(repo, svcScope.Repos) {
			return AccessResult{
				Allowed: false,
				Service: "github",
				Reason:  fmt.Sprintf("repo %q not in scope", repo),
			}
		}

		// Determine operation from the remaining path segments + method
		var subpath string
		if len(parts) > 3 {
			subpath = strings.Join(parts[3:], "/")
		}
		op := mapGitHubOperation(method, subpath)
		if !operationAllowed(op, svcScope.Operations) {
			return AccessResult{
				Allowed:   false,
				Service:   "github",
				Operation: op,
				Reason:    fmt.Sprintf("operation %q not permitted", op),
			}
		}
		return AccessResult{Allowed: true, Service: "github", Operation: op}
	}

	// Non-repo endpoints (e.g., /user, /search) — check if there's a wildcard or general read
	op := mapGitHubGeneral(method)
	if operationAllowed(op, svcScope.Operations) {
		return AccessResult{Allowed: true, Service: "github", Operation: op}
	}
	return AccessResult{Allowed: false, Service: "github", Operation: op, Reason: fmt.Sprintf("operation %q not permitted", op)}
}

// mapGitHubOperation maps an HTTP method + subpath to an operation name.
func mapGitHubOperation(method, subpath string) string {
	// Normalize
	method = strings.ToUpper(method)
	subpath = strings.TrimSuffix(subpath, "/")

	// Remove numeric path segments (PR numbers, issue numbers, etc.)
	normalized := normalizeNumericSegments(subpath)

	switch {
	// Pull requests — order matters: more specific patterns before broader ones
	case strings.HasPrefix(normalized, "pulls/N/reviews") && method == "POST":
		return "create_review"
	case strings.HasPrefix(normalized, "pulls") && method == "GET":
		return "read_prs"
	case normalized == "pulls" && method == "POST":
		return "create_pr_draft"
	case normalized == "pulls/N/merge" && method == "PUT":
		return "merge_pr"
	case strings.HasPrefix(normalized, "pulls/N") && method == "PATCH":
		return "update_pr"

	// Issues
	case strings.HasPrefix(normalized, "issues") && method == "GET":
		return "read_issues"
	case normalized == "issues" && method == "POST":
		return "create_issue"
	case strings.HasPrefix(normalized, "issues/N") && method == "PATCH":
		return "update_issue"
	case strings.HasPrefix(normalized, "issues/N/comments") && method == "POST":
		return "create_comment"

	// Contents / files
	case strings.HasPrefix(normalized, "contents") && method == "GET":
		return "read_contents"
	case strings.HasPrefix(normalized, "contents") && (method == "PUT" || method == "POST"):
		return "write_contents"

	// Git references
	case normalized == "git/refs" && method == "POST":
		return "create_branch"
	case strings.HasPrefix(normalized, "git/refs") && method == "DELETE":
		return "delete_branch"
	case strings.HasPrefix(normalized, "git"):
		if method == "GET" {
			return "read_git"
		}
		return "write_git"

	// Branches
	case strings.HasPrefix(normalized, "branches") && method == "DELETE":
		return "delete_branch"
	case strings.HasPrefix(normalized, "branches"):
		if method == "GET" {
			return "read_branches"
		}
		return "write_branches"

	// Commits
	case strings.HasPrefix(normalized, "commits"):
		return "read_commits"

	// Actions / workflows
	case strings.HasPrefix(normalized, "actions"):
		if method == "GET" {
			return "read_actions"
		}
		return "write_actions"

	// Releases
	case strings.HasPrefix(normalized, "releases"):
		if method == "GET" {
			return "read_releases"
		}
		return "write_releases"

	// Default: read or write based on method
	default:
		if method == "GET" || method == "HEAD" {
			return "read"
		}
		return "write"
	}
}

// mapGitHubGeneral maps non-repo GitHub API calls.
func mapGitHubGeneral(method string) string {
	if method == "GET" || method == "HEAD" {
		return "read"
	}
	return "write"
}

// checkGitLab maps GitLab API URLs to operations and checks them against scope.
func checkGitLab(method, path string, scope internal.Scope) AccessResult {
	svcScope, ok := scope.Services["gitlab"]
	if !ok {
		return AccessResult{Allowed: false, Service: "gitlab", Reason: "gitlab not in scope"}
	}

	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")

	// GitLab API pattern: /api/v4/projects/{id_or_path}/...
	apiIdx := -1
	for i, p := range parts {
		if p == "projects" && i >= 1 && parts[i-1] == "v4" {
			apiIdx = i
			break
		}
	}

	if apiIdx >= 0 && len(parts) > apiIdx+1 {
		// Project identifier — could be numeric ID or URL-encoded "group%2Fproject"
		projectID := parts[apiIdx+1]
		// URL-decode in case it's encoded
		decoded, err := url.PathUnescape(projectID)
		if err == nil {
			projectID = decoded
		}

		if !repoAllowed(projectID, svcScope.Repos) {
			return AccessResult{
				Allowed: false,
				Service: "gitlab",
				Reason:  fmt.Sprintf("project %q not in scope", projectID),
			}
		}

		var subpath string
		if len(parts) > apiIdx+2 {
			subpath = strings.Join(parts[apiIdx+2:], "/")
		}
		op := mapGitLabOperation(method, subpath)
		if !operationAllowed(op, svcScope.Operations) {
			return AccessResult{
				Allowed:   false,
				Service:   "gitlab",
				Operation: op,
				Reason:    fmt.Sprintf("operation %q not permitted", op),
			}
		}
		return AccessResult{Allowed: true, Service: "gitlab", Operation: op}
	}

	// Non-project endpoints
	op := mapGitHubGeneral(method) // same logic
	if operationAllowed(op, svcScope.Operations) {
		return AccessResult{Allowed: true, Service: "gitlab", Operation: op}
	}
	return AccessResult{Allowed: false, Service: "gitlab", Operation: op, Reason: fmt.Sprintf("operation %q not permitted", op)}
}

// mapGitLabOperation maps GitLab API subpaths to operation names.
func mapGitLabOperation(method, subpath string) string {
	method = strings.ToUpper(method)
	normalized := normalizeNumericSegments(subpath)

	switch {
	case strings.HasPrefix(normalized, "merge_requests") && method == "GET":
		return "read_prs"
	case normalized == "merge_requests" && method == "POST":
		return "create_pr_draft"
	case strings.HasPrefix(normalized, "merge_requests/N/merge") && method == "PUT":
		return "merge_pr"
	case strings.HasPrefix(normalized, "merge_requests/N") && method == "PUT":
		return "update_pr"
	case strings.HasPrefix(normalized, "issues") && method == "GET":
		return "read_issues"
	case normalized == "issues" && method == "POST":
		return "create_issue"
	case strings.HasPrefix(normalized, "issues/N") && method == "PUT":
		return "update_issue"

	// Merge request approval (must be before generic merge_requests patterns)
	case strings.HasPrefix(normalized, "merge_requests/N/approve") && method == "POST":
		return "create_review"

	// Comments / notes
	case strings.HasPrefix(normalized, "merge_requests/N/notes") && method == "POST":
		return "create_comment"
	case strings.HasPrefix(normalized, "issues/N/notes") && method == "POST":
		return "create_comment"
	case strings.HasPrefix(normalized, "notes") && method == "POST":
		return "create_comment"

	// Repository operations
	case strings.HasPrefix(normalized, "repository/branches") && method == "POST":
		return "create_branch"
	case strings.HasPrefix(normalized, "repository/branches") && method == "DELETE":
		return "delete_branch"
	case strings.HasPrefix(normalized, "repository"):
		if method == "GET" {
			return "read_contents"
		}
		return "write_contents"

	// Releases
	case strings.HasPrefix(normalized, "releases"):
		if method == "GET" {
			return "read_releases"
		}
		return "write_releases"

	// Pipelines
	case strings.HasPrefix(normalized, "pipelines"):
		if method == "GET" {
			return "read_actions"
		}
		return "write_actions"
	default:
		if method == "GET" || method == "HEAD" {
			return "read"
		}
		return "write"
	}
}

// checkAtlassian handles Jira/Confluence API requests.
func checkAtlassian(method, path string, scope internal.Scope) AccessResult {
	svcScope, ok := scope.Services["atlassian"]
	if !ok {
		return AccessResult{Allowed: false, Service: "atlassian", Reason: "atlassian not in scope"}
	}

	method = strings.ToUpper(method)
	var op string
	if method == "GET" || method == "HEAD" {
		op = "read"
	} else {
		op = "write"
	}

	if !operationAllowed(op, svcScope.Operations) {
		return AccessResult{
			Allowed:   false,
			Service:   "atlassian",
			Operation: op,
			Reason:    fmt.Sprintf("operation %q not permitted", op),
		}
	}
	return AccessResult{Allowed: true, Service: "atlassian", Operation: op}
}

// repoAllowed checks if a repo is in the allowed list. Supports wildcard "*".
func repoAllowed(repo string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, r := range allowed {
		if r == "*" || strings.EqualFold(r, repo) {
			return true
		}
		// Support org-level wildcards like "pulp/*"
		if strings.HasSuffix(r, "/*") {
			prefix := strings.TrimSuffix(r, "/*")
			if strings.HasPrefix(strings.ToLower(repo), strings.ToLower(prefix)+"/") {
				return true
			}
		}
	}
	return false
}

// operationAllowed checks if an operation is in the allowed list. Supports wildcard "*".
func operationAllowed(op string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || a == op {
			return true
		}
	}
	return false
}

// normalizeNumericSegments replaces pure-numeric path segments with "N".
func normalizeNumericSegments(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if isNumeric(p) {
			parts[i] = "N"
		}
	}
	return strings.Join(parts, "/")
}

// isNumeric returns true if s is a non-empty string of digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// HandleGitCredential responds to git credential fill requests.
// It extracts the host and path from the request body and returns
// credentials if the repo is within scope.
func HandleGitCredential(w http.ResponseWriter, r *http.Request, scope internal.Scope, credentials map[string]string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse the git credential protocol input (key=value lines)
	body := make([]byte, 4096)
	n, _ := r.Body.Read(body)
	input := string(body[:n])

	fields := parseGitCredentialInput(input)
	host := fields["host"]
	path := fields["path"]
	protocol := fields["protocol"]

	if protocol == "" {
		protocol = "https"
	}

	// Determine which service this is for
	var service string
	switch {
	case strings.Contains(host, "github.com"):
		service = "github"
	case strings.Contains(host, "gitlab"):
		service = "gitlab"
	default:
		http.Error(w, "unknown git host", http.StatusForbidden)
		return
	}

	// Check if the repo is in scope
	svcScope, ok := scope.Services[service]
	if !ok {
		http.Error(w, fmt.Sprintf("service %q not in scope", service), http.StatusForbidden)
		return
	}

	// path is typically "owner/repo.git" — strip .git suffix
	repoPath := strings.TrimSuffix(path, ".git")
	if !repoAllowed(repoPath, svcScope.Repos) {
		http.Error(w, fmt.Sprintf("repo %q not in scope", repoPath), http.StatusForbidden)
		return
	}

	// Look up credentials
	cred, ok := credentials[service]
	if !ok {
		http.Error(w, "no credentials for service", http.StatusInternalServerError)
		return
	}

	// Respond in git credential protocol format
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "protocol=%s\n", protocol)
	fmt.Fprintf(w, "host=%s\n", host)
	if service == "github" {
		fmt.Fprintf(w, "username=x-access-token\n")
	} else {
		fmt.Fprintf(w, "username=oauth2\n")
	}
	fmt.Fprintf(w, "password=%s\n", cred)
}

// parseGitCredentialInput parses key=value lines from git credential protocol input.
func parseGitCredentialInput(input string) map[string]string {
	result := make(map[string]string)
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	return result
}
