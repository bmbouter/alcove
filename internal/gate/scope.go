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
	"regexp"
	"strings"

	"github.com/bmbouter/alcove/internal"
)

// issueKeyRegexp matches JIRA issue keys like PROJ-123, ABC-1, MYPROJECT-999.
var issueKeyRegexp = regexp.MustCompile(`^[A-Z][A-Z0-9]+-\d+$`)

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

	// Splunk — check if "splunk" is in scope and URL path looks like a Splunk API
	if _, ok := scope.Services["splunk"]; ok {
		if strings.HasPrefix(u.Path, "/services/") || strings.HasPrefix(u.Path, "/servicesNS/") {
			return checkSplunk(method, u.Path, scope)
		}
	}

	return AccessResult{Allowed: false, Service: "unknown", Reason: fmt.Sprintf("host %q is not a recognized service", host)}
}

// CheckServiceAccess evaluates whether a request is permitted for a known
// service name. This is used by proxy endpoints where the service identity is
// already known from the URL prefix (e.g., /splunk/) rather than derived from
// the target hostname.
func CheckServiceAccess(method, path string, service string, scope internal.Scope) AccessResult {
	switch service {
	case "github":
		return checkGitHub(method, path, scope)
	case "gitlab":
		return checkGitLab(method, path, scope)
	case "jira":
		return checkAtlassian(method, path, scope)
	case "splunk":
		return checkSplunk(method, path, scope)
	default:
		return AccessResult{Allowed: false, Service: service, Reason: fmt.Sprintf("service %q has no scope handler", service)}
	}
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

	// Issues and labels
	case strings.HasPrefix(normalized, "issues") && method == "GET":
		return "read_issues"
	case normalized == "issues" && method == "POST":
		return "create_issue"
	case strings.HasPrefix(normalized, "issues/N") && method == "PATCH":
		return "update_issue"
	case strings.HasPrefix(normalized, "issues/N/comments") && method == "POST":
		return "create_comment"
	case strings.HasPrefix(normalized, "issues/N/labels") && (method == "POST" || method == "DELETE"):
		return "update_issue"
	case strings.HasPrefix(normalized, "labels") && method == "GET":
		return "read_issues"

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
	// Check both "jira" and "atlassian" service names for backward compat.
	svcScope, ok := scope.Services["jira"]
	serviceName := "jira"
	if !ok {
		svcScope, ok = scope.Services["atlassian"]
		serviceName = "atlassian"
	}
	if !ok {
		return AccessResult{Allowed: false, Service: "jira", Reason: "jira not in scope"}
	}

	method = strings.ToUpper(method)
	op := mapJiraOperation(method, strings.TrimPrefix(path, "/"))

	// Extract project key from issue keys in the path and check against allowed repos/projects.
	if len(svcScope.Repos) > 0 {
		if projectKey := extractJiraProjectKey(path); projectKey != "" {
			if !repoAllowed(projectKey, svcScope.Repos) {
				return AccessResult{
					Allowed: false,
					Service: serviceName,
					Reason:  fmt.Sprintf("project %q not in scope", projectKey),
				}
			}
		}
	}

	if !operationAllowed(op, svcScope.Operations) {
		return AccessResult{
			Allowed:   false,
			Service:   serviceName,
			Operation: op,
			Reason:    fmt.Sprintf("operation %q not permitted", op),
		}
	}
	return AccessResult{Allowed: true, Service: serviceName, Operation: op}
}

// mapJiraOperation maps an HTTP method + API path to a JIRA operation name.
// The path should be the API path without leading slash (e.g., "rest/api/3/issue/PROJ-123").
func mapJiraOperation(method, path string) string {
	method = strings.ToUpper(method)
	path = strings.TrimSuffix(path, "/")
	normalized := normalizeJiraPath(path)

	switch {
	// Read operations — more specific patterns first

	// Transitions: GET rest/api/*/issue/ISSUE/transitions
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/transitions") && method == "GET":
		return "read_transitions"

	// Comments: GET rest/api/*/issue/ISSUE/comment*
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/comment") && method == "GET":
		return "read_comments"
	case matchJiraPathPrefix(normalized, "rest/api/*/issue/ISSUE/comment/") && method == "GET":
		return "read_comments"

	// Issues: GET rest/api/*/issue or rest/api/*/issue/*
	case matchJiraPath(normalized, "rest/api/*/issue") && method == "GET":
		return "read_issues"
	case matchJiraPathPrefix(normalized, "rest/api/*/issue/ISSUE") && method == "GET":
		return "read_issues"

	// Search: GET or POST rest/api/*/search*
	case matchJiraPathPrefix(normalized, "rest/api/*/search") && (method == "GET" || method == "POST"):
		return "search_issues"

	// Projects: GET rest/api/*/project*
	case matchJiraPathPrefix(normalized, "rest/api/*/project") && method == "GET":
		return "read_projects"

	// Boards: GET rest/agile/*/board*
	case matchJiraPathPrefix(normalized, "rest/agile/*/board") && method == "GET":
		return "read_boards"

	// Sprints: GET rest/agile/*/sprint*
	case matchJiraPathPrefix(normalized, "rest/agile/*/sprint") && method == "GET":
		return "read_sprints"

	// Metadata: GET rest/api/*/issuetype, priority, status, field, label, myself, user*
	case matchJiraPathPrefix(normalized, "rest/api/*/issuetype") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/priority") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/status") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/field") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/label") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/myself") && method == "GET":
		return "read_metadata"
	case matchJiraPathPrefix(normalized, "rest/api/*/user") && method == "GET":
		return "read_metadata"

	// Write operations — more specific patterns first

	// Transition issue: POST rest/api/*/issue/ISSUE/transitions
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/transitions") && method == "POST":
		return "transition_issue"

	// Add comment: POST rest/api/*/issue/ISSUE/comment
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/comment") && method == "POST":
		return "add_comment"

	// Update comment: PUT rest/api/*/issue/ISSUE/comment/*
	case matchJiraPathPrefix(normalized, "rest/api/*/issue/ISSUE/comment/") && method == "PUT":
		return "update_comment"

	// Delete comment: DELETE rest/api/*/issue/ISSUE/comment/*
	case matchJiraPathPrefix(normalized, "rest/api/*/issue/ISSUE/comment/") && method == "DELETE":
		return "delete_comment"

	// Assign issue: PUT rest/api/*/issue/ISSUE/assignee
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/assignee") && method == "PUT":
		return "assign_issue"

	// Add worklog: POST rest/api/*/issue/ISSUE/worklog
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE/worklog") && method == "POST":
		return "add_worklog"

	// Create issue: POST rest/api/*/issue (not /issue/SOMETHING)
	case matchJiraPath(normalized, "rest/api/*/issue") && method == "POST":
		return "create_issue"

	// Delete issue: DELETE rest/api/*/issue/*
	case matchJiraPathPrefix(normalized, "rest/api/*/issue/ISSUE") && method == "DELETE":
		return "delete_issue"

	// Update issue: PUT rest/api/*/issue/ISSUE (not /issue/ISSUE/comment etc.)
	case matchJiraPath(normalized, "rest/api/*/issue/ISSUE") && method == "PUT":
		return "update_issue"

	// Move to sprint: POST rest/agile/*/sprint/N/issue
	case matchJiraPathSprint(normalized) && method == "POST":
		return "move_to_sprint"

	// Read catch-all for any rest/* path
	case strings.HasPrefix(normalized, "rest/") && (method == "GET" || method == "HEAD"):
		return "read"

	// Write catch-all
	default:
		if method == "GET" || method == "HEAD" {
			return "read"
		}
		return "write"
	}
}

// checkSplunk maps Splunk API URLs to operations and checks them against scope.
func checkSplunk(method, path string, scope internal.Scope) AccessResult {
	svcScope, ok := scope.Services["splunk"]
	if !ok {
		return AccessResult{Allowed: false, Service: "splunk", Reason: "splunk not in scope"}
	}

	method = strings.ToUpper(method)
	op := mapSplunkOperation(method, strings.TrimPrefix(path, "/"))

	if !operationAllowed(op, svcScope.Operations) {
		return AccessResult{
			Allowed:   false,
			Service:   "splunk",
			Operation: op,
			Reason:    fmt.Sprintf("operation %q not permitted", op),
		}
	}
	return AccessResult{Allowed: true, Service: "splunk", Operation: op}
}

// mapSplunkOperation maps an HTTP method + API path to a Splunk operation name.
func mapSplunkOperation(method, path string) string {
	method = strings.ToUpper(method)
	path = strings.TrimSuffix(path, "/")
	normalized := normalizeNumericSegments(path)

	// Strip servicesNS/{user}/{app}/ prefix to get the effective path.
	if strings.HasPrefix(normalized, "servicesNS/") {
		parts := strings.SplitN(normalized, "/", 4)
		if len(parts) >= 4 {
			normalized = parts[3]
		}
	}

	// Strip services/ prefix for matching.
	effective := strings.TrimPrefix(normalized, "services/")

	switch {
	// Search job results: GET search/jobs/*/results
	case strings.HasPrefix(effective, "search/jobs/") && strings.HasSuffix(effective, "/results"):
		return "read_results"

	// Search jobs: POST search/jobs (create a search), GET search/jobs/* (read search status/details)
	case effective == "search/jobs" && method == "POST":
		return "search"
	case strings.HasPrefix(effective, "search/jobs"):
		if method == "GET" {
			return "search"
		}
		return "write"

	// Saved searches
	case strings.HasPrefix(effective, "saved/searches"):
		if method == "GET" {
			return "read_saved_searches"
		}
		return "write"

	// Alerts
	case strings.HasPrefix(effective, "alerts"):
		if method == "GET" {
			return "read_alerts"
		}
		return "write"

	// Default: read or write based on method
	default:
		if method == "GET" || method == "HEAD" {
			return "read"
		}
		return "write"
	}
}

// normalizeJiraPath replaces JIRA issue keys (e.g., PROJ-123) with "ISSUE"
// for pattern matching, and numeric segments with "N".
func normalizeJiraPath(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if issueKeyRegexp.MatchString(p) {
			parts[i] = "ISSUE"
		} else if isNumeric(p) {
			parts[i] = "N"
		}
	}
	return strings.Join(parts, "/")
}

// matchJiraPath checks if a normalized path matches a pattern with wildcard
// support. The pattern uses "*" to match a single path segment (e.g., API version).
func matchJiraPath(normalized, pattern string) bool {
	nParts := strings.Split(normalized, "/")
	pParts := strings.Split(pattern, "/")
	if len(nParts) != len(pParts) {
		return false
	}
	for i, pp := range pParts {
		if pp == "*" {
			continue
		}
		if nParts[i] != pp {
			return false
		}
	}
	return true
}

// matchJiraPathPrefix checks if a normalized path starts with a pattern prefix.
// The pattern uses "*" to match a single path segment.
func matchJiraPathPrefix(normalized, pattern string) bool {
	// For prefix matching, the normalized path must have at least as many segments
	// as the pattern, and all pattern segments must match.
	pattern = strings.TrimSuffix(pattern, "/")
	nParts := strings.Split(normalized, "/")
	pParts := strings.Split(pattern, "/")
	if len(nParts) < len(pParts) {
		return false
	}
	for i, pp := range pParts {
		if pp == "*" {
			continue
		}
		if nParts[i] != pp {
			return false
		}
	}
	return true
}

// matchJiraPathSprint matches rest/agile/*/sprint/N/issue pattern.
func matchJiraPathSprint(normalized string) bool {
	parts := strings.Split(normalized, "/")
	// rest/agile/{version}/sprint/{id}/issue = 6 parts
	if len(parts) != 6 {
		return false
	}
	return parts[0] == "rest" && parts[1] == "agile" && parts[3] == "sprint" && parts[5] == "issue"
}

// extractJiraProjectKey extracts a JIRA project key from issue keys in the path.
// For example, "rest/api/3/issue/PROJ-123" returns "PROJ".
func extractJiraProjectKey(path string) string {
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if issueKeyRegexp.MatchString(p) {
			idx := strings.LastIndex(p, "-")
			if idx > 0 {
				return p[:idx]
			}
		}
	}
	return ""
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
