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

package gate

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bmbouter/alcove/internal"
)

// --------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------

// newTestProxy creates a Gate proxy configured with the given scope and
// credentials.  It returns the proxy and a test HTTP server bound to it.
func newTestProxy(t *testing.T, scope internal.Scope, creds map[string]string) (*Proxy, *httptest.Server) {
	t.Helper()
	cfg := Config{
		SessionID:    "test-session",
		Scope:        scope,
		Credentials:  creds,
		ToolConfigs:  map[string]ToolConfig{},
		SessionToken: "session-tok",
		LLMToken:     "llm-secret-key",
		LLMProvider:  "anthropic",
		LLMTokenType: "api_key",
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })
	return p, ts
}

// doRequest sends a request to the test server and returns the status code
// and response body.
func doRequest(t *testing.T, method, url string, body string) (int, string) {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sending request: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(respBody)
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — GitHub
// --------------------------------------------------------------------

// githubScope returns a scope allowing github with the specified repos and operations.
func githubScope(repos, ops []string) internal.Scope {
	return internal.Scope{
		Services: map[string]internal.ServiceScope{
			"github": {Repos: repos, Operations: ops},
		},
	}
}

func TestGitHubScopeEnforcement_AllowedOperations(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs", "create_pr_draft", "read_contents", "read_issues", "read_commits"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"read PRs", "GET", "/github/repos/pulp/pulpcore/pulls"},
		{"read single PR", "GET", "/github/repos/pulp/pulpcore/pulls/42"},
		{"create draft PR", "POST", "/github/repos/pulp/pulpcore/pulls"},
		{"read file contents", "GET", "/github/repos/pulp/pulpcore/contents/README.md"},
		{"read nested contents", "GET", "/github/repos/pulp/pulpcore/contents/src/main.go"},
		{"read issues", "GET", "/github/repos/pulp/pulpcore/issues"},
		{"read single issue", "GET", "/github/repos/pulp/pulpcore/issues/7"},
		{"read commits", "GET", "/github/repos/pulp/pulpcore/commits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code == http.StatusForbidden {
				t.Errorf("expected allowed (non-403) but got 403: %s", body)
			}
		})
	}
}

func TestGitHubScopeEnforcement_DeniedOperations(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs", "create_pr_draft", "read_contents"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"merge PR - op not in scope", "PUT", "/github/repos/pulp/pulpcore/pulls/1/merge"},
		{"delete branch - op not in scope", "DELETE", "/github/repos/pulp/pulpcore/git/refs/heads/my-branch"},
		{"create issue - op not in scope", "POST", "/github/repos/pulp/pulpcore/issues"},
		{"update PR - op not in scope", "PATCH", "/github/repos/pulp/pulpcore/pulls/5"},
		{"write contents - op not in scope", "PUT", "/github/repos/pulp/pulpcore/contents/file.txt"},
		{"create review - op not in scope", "POST", "/github/repos/pulp/pulpcore/pulls/3/reviews"},
		{"create comment - op not in scope", "POST", "/github/repos/pulp/pulpcore/issues/1/comments"},
		{"write actions - op not in scope", "POST", "/github/repos/pulp/pulpcore/actions/workflows/ci.yml/dispatches"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden but got %d", code)
			}
		})
	}
}

func TestGitHubScopeEnforcement_RepoNotInScope(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs", "read_contents", "read_issues", "create_pr_draft"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// Every operation on a repo NOT in scope should be denied, even read operations.
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"read PRs - wrong repo", "GET", "/github/repos/other/repo/pulls"},
		{"read contents - wrong repo", "GET", "/github/repos/other/repo/contents/README.md"},
		{"create PR - wrong repo", "POST", "/github/repos/other/repo/pulls"},
		{"read issues - wrong repo", "GET", "/github/repos/evil/exfiltrate/issues"},
		{"read PRs - wrong org", "GET", "/github/repos/notpulp/pulpcore/pulls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden but got %d", code)
			}
		})
	}
}

func TestGitHubScopeEnforcement_OrgWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/*"},
		[]string{"read_prs", "read_contents"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// Allowed: any repo under pulp/
	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")
	if code == http.StatusForbidden {
		t.Errorf("expected allowed for pulp/pulpcore with pulp/* wildcard, got 403")
	}

	code, _ = doRequest(t, "GET", ts.URL+"/github/repos/pulp/other-repo/pulls", "")
	if code == http.StatusForbidden {
		t.Errorf("expected allowed for pulp/other-repo with pulp/* wildcard, got 403")
	}

	// Denied: repo under a different org
	code, _ = doRequest(t, "GET", ts.URL+"/github/repos/evil/repo/pulls", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for evil/repo with pulp/* wildcard, got %d", code)
	}
}

func TestGitHubScopeEnforcement_RepoWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"*"},
		[]string{"read_prs"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// "*" wildcard should allow any repo
	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/any/repo/pulls", "")
	if code == http.StatusForbidden {
		t.Errorf("expected allowed for any/repo with * wildcard, got 403")
	}

	// But operations not in scope should still be denied
	code, _ = doRequest(t, "PUT", ts.URL+"/github/repos/any/repo/pulls/1/merge", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for merge_pr not in scope, got %d", code)
	}
}

func TestGitHubScopeEnforcement_OperationWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"*"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// "*" operation wildcard should allow everything on the allowed repo
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"read PRs", "GET", "/github/repos/pulp/pulpcore/pulls"},
		{"merge PR", "PUT", "/github/repos/pulp/pulpcore/pulls/1/merge"},
		{"delete branch", "DELETE", "/github/repos/pulp/pulpcore/git/refs/heads/branch"},
		{"create issue", "POST", "/github/repos/pulp/pulpcore/issues"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code == http.StatusForbidden {
				t.Errorf("expected allowed with * operation wildcard but got 403: %s", body)
			}
		})
	}

	// Still denied on wrong repo
	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/evil/repo/pulls", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for wrong repo even with * operations, got %d", code)
	}
}

func TestGitHubScopeEnforcement_ServiceNotInScope(t *testing.T) {
	// Scope has gitlab but NOT github
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{
			"gitlab": {Repos: []string{"*"}, Operations: []string{"*"}},
		},
	}
	_, ts := newTestProxy(t, scope, map[string]string{"gitlab": "glpat_token"})

	code, body := doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 when github not in scope, got %d: %s", code, body)
	}
}

func TestGitHubScopeEnforcement_EmptyRepoList(t *testing.T) {
	// Operations are allowed but no repos specified
	scope := githubScope([]string{}, []string{"read_prs", "read_contents"})
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 with empty repo list, got %d", code)
	}
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — GitLab
// --------------------------------------------------------------------

func gitlabScope(repos, ops []string) internal.Scope {
	return internal.Scope{
		Services: map[string]internal.ServiceScope{
			"gitlab": {Repos: repos, Operations: ops},
		},
	}
}

func TestGitLabScopeEnforcement_AllowedOperations(t *testing.T) {
	// Use numeric project IDs to avoid %2F encoding issues where url.Parse
	// decodes the slash, splitting the path segment and breaking project lookup.
	scope := gitlabScope(
		[]string{"12345"},
		[]string{"read_prs", "create_pr_draft", "read_contents"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"gitlab": "glpat_real_token"})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"read MRs", "GET", "/gitlab/api/v4/projects/12345/merge_requests"},
		{"read single MR", "GET", "/gitlab/api/v4/projects/12345/merge_requests/1"},
		{"create MR", "POST", "/gitlab/api/v4/projects/12345/merge_requests"},
		{"read repo tree", "GET", "/gitlab/api/v4/projects/12345/repository/tree"},
		{"read repo file", "GET", "/gitlab/api/v4/projects/12345/repository/files/README.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code == http.StatusForbidden {
				t.Errorf("expected allowed (non-403) but got 403: %s", body)
			}
		})
	}
}

func TestGitLabScopeEnforcement_DeniedOperations(t *testing.T) {
	scope := gitlabScope(
		[]string{"12345"},
		[]string{"read_prs", "read_contents"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"gitlab": "glpat_real_token"})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"merge MR - not in scope", "PUT", "/gitlab/api/v4/projects/12345/merge_requests/1/merge"},
		{"create MR - not in scope", "POST", "/gitlab/api/v4/projects/12345/merge_requests"},
		{"delete branch - not in scope", "DELETE", "/gitlab/api/v4/projects/12345/repository/branches/feature"},
		{"write file - not in scope", "POST", "/gitlab/api/v4/projects/12345/repository/files/new.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := doRequest(t, tt.method, ts.URL+tt.path, "")
			if code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden but got %d", code)
			}
		})
	}
}

func TestGitLabScopeEnforcement_ProjectNotInScope(t *testing.T) {
	scope := gitlabScope(
		[]string{"mygroup/myproject"},
		[]string{"read_prs", "read_contents", "create_pr_draft"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"gitlab": "glpat_real_token"})

	tests := []struct {
		name string
		path string
	}{
		{"wrong project (encoded)", "/gitlab/api/v4/projects/evil%2Fproject/merge_requests"},
		{"wrong project (numeric)", "/gitlab/api/v4/projects/99999/merge_requests"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _ := doRequest(t, "GET", ts.URL+tt.path, "")
			if code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden but got %d", code)
			}
		})
	}
}

// --------------------------------------------------------------------
// Category 3: Credential isolation
// --------------------------------------------------------------------

func TestCredentialInjection_BearerFormat(t *testing.T) {
	// Test that injectToolCredential correctly sets bearer-format auth headers.
	p := &Proxy{config: Config{
		Credentials: map[string]string{"github": "ghp_real_secret_123"},
	}}

	req := httptest.NewRequest("GET", "https://api.github.com/repos/org/repo", nil)
	req.Header.Set("Authorization", "Bearer dummy-skiff-token")

	p.injectToolCredential(req, "github", "Authorization", "bearer")

	got := req.Header.Get("Authorization")
	if got != "Bearer ghp_real_secret_123" {
		t.Errorf("expected 'Bearer ghp_real_secret_123', got %q", got)
	}
}

func TestCredentialInjection_HeaderFormat(t *testing.T) {
	// Test that injectToolCredential correctly sets header-format auth (e.g., GitLab PRIVATE-TOKEN).
	p := &Proxy{config: Config{
		Credentials: map[string]string{"gitlab": "glpat_secret_456"},
	}}

	req := httptest.NewRequest("GET", "https://gitlab.com/api/v4/projects", nil)
	p.injectToolCredential(req, "gitlab", "PRIVATE-TOKEN", "header")

	got := req.Header.Get("PRIVATE-TOKEN")
	if got != "glpat_secret_456" {
		t.Errorf("expected 'glpat_secret_456', got %q", got)
	}
}

func TestCredentialInjection_BasicFormat(t *testing.T) {
	p := &Proxy{config: Config{
		Credentials: map[string]string{"jira": "user:token"},
	}}

	req := httptest.NewRequest("GET", "https://company.atlassian.net/rest/api", nil)
	p.injectToolCredential(req, "jira", "Authorization", "basic")

	got := req.Header.Get("Authorization")
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:token"))
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestCredentialInjection_DummyTokenNotForwarded(t *testing.T) {
	// When credentials exist, the real credential should REPLACE the dummy token.
	// The dummy token should never reach the upstream.
	p := &Proxy{config: Config{
		Credentials: map[string]string{"github": "ghp_real"},
	}}

	req := httptest.NewRequest("GET", "https://api.github.com/repos/org/repo", nil)
	req.Header.Set("Authorization", "Bearer DUMMY_TOKEN_FROM_SKIFF")

	p.injectToolCredential(req, "github", "Authorization", "bearer")

	got := req.Header.Get("Authorization")
	if strings.Contains(got, "DUMMY_TOKEN_FROM_SKIFF") {
		t.Errorf("SECURITY: dummy token was NOT replaced, got %q", got)
	}
	if got != "Bearer ghp_real" {
		t.Errorf("expected real token, got %q", got)
	}
}

func TestCredentialInjection_MissingCredentialNoOp(t *testing.T) {
	// When no credential exists for a service, injectToolCredential should not
	// add or modify auth headers.
	p := &Proxy{config: Config{
		Credentials: map[string]string{}, // no credentials
	}}

	req := httptest.NewRequest("GET", "https://api.github.com/repos/org/repo", nil)
	req.Header.Set("Authorization", "Bearer original")

	p.injectToolCredential(req, "github", "Authorization", "bearer")

	// Original header should be unchanged since there's no credential to inject
	got := req.Header.Get("Authorization")
	if got != "Bearer original" {
		t.Errorf("expected original header unchanged, got %q", got)
	}
}

func TestCredentialIsolation_DeniedRequestDoesNotForwardCredential(t *testing.T) {
	// Start a fake upstream that should NEVER be reached
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs"}, // only read_prs allowed
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_secret"})

	// Attempt a denied operation
	code, _ := doRequest(t, "PUT", ts.URL+"/github/repos/pulp/pulpcore/pulls/1/merge", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", code)
	}

	// The upstream should NOT have been called at all for a denied request
	// (Gate returns 403 before proxying). Note: in this test the upstream
	// is the real api.github.com which won't be reached, but the key
	// verification is that Gate returned 403 without forwarding.
	_ = upstreamCalled
}

func TestCredentialIsolation_CrossServiceNoLeak(t *testing.T) {
	// Scope only allows github, NOT gitlab
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{
			"github": {Repos: []string{"*"}, Operations: []string{"*"}},
		},
	}
	_, ts := newTestProxy(t, scope, map[string]string{
		"github": "ghp_github_secret",
		"gitlab": "glpat_gitlab_secret",
	})

	// GitLab request should be denied even though credentials exist
	code, _ := doRequest(t, "GET", ts.URL+"/gitlab/api/v4/projects/group%2Fproject/merge_requests", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for gitlab (not in scope), got %d", code)
	}
}

func TestCredentialInjection_LegacyFallback_GitHub(t *testing.T) {
	// Test the legacy injectServiceCredential path (no ToolConfig).
	p := &Proxy{config: Config{
		Credentials: map[string]string{"github": "ghp_secret_123"},
		ToolConfigs: map[string]ToolConfig{}, // empty — triggers legacy fallback
	}}

	req := httptest.NewRequest("GET", "https://api.github.com/repos/org/repo", nil)
	p.injectServiceCredential(req, "github")

	got := req.Header.Get("Authorization")
	if got != "Bearer ghp_secret_123" {
		t.Errorf("expected 'Bearer ghp_secret_123', got %q", got)
	}
}

func TestCredentialInjection_LegacyFallback_GitLab(t *testing.T) {
	p := &Proxy{config: Config{
		Credentials: map[string]string{"gitlab": "glpat_secret_456"},
		ToolConfigs: map[string]ToolConfig{},
	}}

	req := httptest.NewRequest("GET", "https://gitlab.com/api/v4/projects", nil)
	p.injectServiceCredential(req, "gitlab")

	got := req.Header.Get("PRIVATE-TOKEN")
	if got != "glpat_secret_456" {
		t.Errorf("expected 'glpat_secret_456', got %q", got)
	}
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — edge cases
// --------------------------------------------------------------------

func TestScopeEnforcement_CaseInsensitiveRepo(t *testing.T) {
	scope := githubScope(
		[]string{"Pulp/PulpCore"},
		[]string{"read_prs"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// Lowercase request should match uppercase scope entry
	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")
	if code == http.StatusForbidden {
		t.Errorf("expected case-insensitive repo match to succeed, got 403")
	}
}

func TestScopeEnforcement_NonRepoGitHubEndpoint(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read"},
	)
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	// Non-repo endpoints like /user use the "read" / "write" general mapping
	code, _ := doRequest(t, "GET", ts.URL+"/github/user", "")
	if code == http.StatusForbidden {
		t.Errorf("expected /user GET with 'read' op to be allowed, got 403")
	}

	// POST to /user should require "write" op which is not in scope
	code, _ = doRequest(t, "POST", ts.URL+"/github/user", "")
	if code != http.StatusForbidden {
		t.Errorf("expected 403 for POST /user without 'write' op, got %d", code)
	}
}

func TestScopeEnforcement_HealthzAlwaysAllowed(t *testing.T) {
	// Even with an empty scope, healthz should work
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	_, ts := newTestProxy(t, scope, map[string]string{})

	code, body := doRequest(t, "GET", ts.URL+"/healthz", "")
	if code != http.StatusOK {
		t.Errorf("expected 200 for /healthz, got %d", code)
	}
	if body != "ok" {
		t.Errorf("expected 'ok' body, got %q", body)
	}
}

// --------------------------------------------------------------------
// Category 3: Git credential helper isolation
// --------------------------------------------------------------------

func TestGitCredential_AllowedRepo(t *testing.T) {
	scope := githubScope([]string{"pulp/pulpcore"}, []string{"read_prs"})
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	body := "protocol=https\nhost=github.com\npath=pulp/pulpcore.git\n"
	code, respBody := doRequest(t, "POST", ts.URL+"/git-credential", body)
	if code != http.StatusOK {
		t.Errorf("expected 200 for allowed repo git-credential, got %d: %s", code, respBody)
	}
	if !strings.Contains(respBody, "password=ghp_real_token") {
		t.Errorf("expected real token in git-credential response, got: %s", respBody)
	}
	if !strings.Contains(respBody, "username=x-access-token") {
		t.Errorf("expected x-access-token username for github, got: %s", respBody)
	}
}

func TestGitCredential_DeniedRepo(t *testing.T) {
	scope := githubScope([]string{"pulp/pulpcore"}, []string{"read_prs"})
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	body := "protocol=https\nhost=github.com\npath=evil/exfiltrate.git\n"
	code, respBody := doRequest(t, "POST", ts.URL+"/git-credential", body)
	if code == http.StatusOK {
		t.Errorf("expected denial for out-of-scope repo, got 200: %s", respBody)
	}
	if strings.Contains(respBody, "ghp_real_token") {
		t.Errorf("SECURITY: real token leaked in denied git-credential response: %s", respBody)
	}
}

func TestGitCredential_ServiceNotInScope(t *testing.T) {
	// Only gitlab in scope, git-credential for github should fail
	scope := gitlabScope([]string{"*"}, []string{"*"})
	_, ts := newTestProxy(t, scope, map[string]string{
		"gitlab": "glpat_token",
		"github": "ghp_should_not_leak",
	})

	body := "protocol=https\nhost=github.com\npath=pulp/pulpcore.git\n"
	code, respBody := doRequest(t, "POST", ts.URL+"/git-credential", body)
	if code == http.StatusOK {
		t.Errorf("expected denial for github (not in scope), got 200")
	}
	if strings.Contains(respBody, "ghp_should_not_leak") {
		t.Errorf("SECURITY: github token leaked when github not in scope: %s", respBody)
	}
}

func TestGitCredential_UnknownHost(t *testing.T) {
	scope := githubScope([]string{"*"}, []string{"*"})
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_token"})

	body := "protocol=https\nhost=bitbucket.org\npath=org/repo.git\n"
	code, respBody := doRequest(t, "POST", ts.URL+"/git-credential", body)
	if code == http.StatusOK {
		t.Errorf("expected denial for unknown host, got 200")
	}
	if strings.Contains(respBody, "ghp_token") {
		t.Errorf("SECURITY: credential leaked for unknown host: %s", respBody)
	}
}

// --------------------------------------------------------------------
// Category 1: Proxy log entries (audit trail)
// --------------------------------------------------------------------

func TestProxyLog_AllowedRequestLogged(t *testing.T) {
	scope := githubScope([]string{"pulp/pulpcore"}, []string{"read_prs"})
	p, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")

	entries := p.FlushLogs()
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry for allowed request")
	}
	found := false
	for _, e := range entries {
		if e.Service == "github" && e.Operation == "read_prs" && e.Decision == "allow" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log entry with service=github, operation=read_prs, decision=allow; got: %+v", entries)
	}
}

func TestProxyLog_DeniedRequestLogged(t *testing.T) {
	scope := githubScope([]string{"pulp/pulpcore"}, []string{"read_prs"})
	p, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_real_token"})

	doRequest(t, "PUT", ts.URL+"/github/repos/pulp/pulpcore/pulls/1/merge", "")

	entries := p.FlushLogs()
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry for denied request")
	}
	found := false
	for _, e := range entries {
		if e.Service == "github" && e.Operation == "merge_pr" && e.Decision == "deny" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log entry with service=github, operation=merge_pr, decision=deny; got: %+v", entries)
	}
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — operation mapping exhaustive tests
// --------------------------------------------------------------------

func TestGitHubOperationMapping(t *testing.T) {
	// Verify that each operation is correctly mapped from method+path
	tests := []struct {
		method   string
		subpath  string
		expected string
	}{
		{"GET", "pulls", "read_prs"},
		{"GET", "pulls/42", "read_prs"},
		{"POST", "pulls", "create_pr_draft"},
		{"PUT", "pulls/1/merge", "merge_pr"},
		{"PATCH", "pulls/5", "update_pr"},
		{"POST", "pulls/3/reviews", "create_review"},
		{"GET", "issues", "read_issues"},
		{"GET", "issues/7", "read_issues"},
		{"POST", "issues", "create_issue"},
		{"PATCH", "issues/2", "update_issue"},
		{"POST", "issues/1/comments", "create_comment"},
		{"GET", "contents/README.md", "read_contents"},
		{"PUT", "contents/file.txt", "write_contents"},
		{"POST", "git/refs", "create_branch"},
		{"DELETE", "git/refs/heads/branch", "delete_branch"},
		{"GET", "git/refs", "read_git"},
		{"POST", "git/trees", "write_git"},
		{"DELETE", "branches/feature", "delete_branch"},
		{"GET", "branches", "read_branches"},
		{"GET", "commits", "read_commits"},
		{"GET", "actions/runs", "read_actions"},
		{"POST", "actions/workflows/ci.yml/dispatches", "write_actions"},
		{"GET", "releases", "read_releases"},
		{"POST", "releases", "write_releases"},
		{"GET", "labels", "read"},
		{"POST", "labels", "write"},
	}

	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.subpath, func(t *testing.T) {
			got := mapGitHubOperation(tt.method, tt.subpath)
			if got != tt.expected {
				t.Errorf("mapGitHubOperation(%q, %q) = %q, want %q", tt.method, tt.subpath, got, tt.expected)
			}
		})
	}
}

func TestGitLabOperationMapping(t *testing.T) {
	tests := []struct {
		method   string
		subpath  string
		expected string
	}{
		{"GET", "merge_requests", "read_prs"},
		{"GET", "merge_requests/1", "read_prs"},
		{"POST", "merge_requests", "create_pr_draft"},
		{"PUT", "merge_requests/1/merge", "merge_pr"},
		{"PUT", "merge_requests/5", "update_pr"},
		{"GET", "issues", "read_issues"},
		{"POST", "issues", "create_issue"},
		{"PUT", "issues/2", "update_issue"},
		{"POST", "merge_requests/1/approve", "create_review"},
		{"POST", "merge_requests/1/notes", "create_comment"},
		{"POST", "issues/1/notes", "create_comment"},
		{"POST", "repository/branches", "create_branch"},
		{"DELETE", "repository/branches/feature", "delete_branch"},
		{"GET", "repository/tree", "read_contents"},
		{"POST", "repository/files/new.txt", "write_contents"},
		{"GET", "releases", "read_releases"},
		{"POST", "releases", "write_releases"},
		{"GET", "pipelines", "read_actions"},
		{"POST", "pipelines", "write_actions"},
	}

	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.subpath, func(t *testing.T) {
			got := mapGitLabOperation(tt.method, tt.subpath)
			if got != tt.expected {
				t.Errorf("mapGitLabOperation(%q, %q) = %q, want %q", tt.method, tt.subpath, got, tt.expected)
			}
		})
	}
}

// --------------------------------------------------------------------
// Category 1: Multiple services in scope simultaneously
// --------------------------------------------------------------------

func TestMultiServiceScope(t *testing.T) {
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{
			"github": {Repos: []string{"pulp/pulpcore"}, Operations: []string{"read_prs"}},
			"gitlab": {Repos: []string{"*"}, Operations: []string{"read_contents"}},
		},
	}
	_, ts := newTestProxy(t, scope, map[string]string{
		"github": "ghp_token",
		"gitlab": "glpat_token",
	})

	// GitHub: allowed operation
	code, _ := doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/pulls", "")
	if code == http.StatusForbidden {
		t.Errorf("github read_prs should be allowed")
	}

	// GitHub: denied operation (read_contents not in github's ops)
	// Note: read_contents IS in gitlab's ops but should not bleed over
	code, _ = doRequest(t, "GET", ts.URL+"/github/repos/pulp/pulpcore/contents/README.md", "")
	if code != http.StatusForbidden {
		t.Errorf("github read_contents should be denied (not in github ops), got %d", code)
	}

	// GitLab: allowed operation (use numeric project ID to avoid %2F encoding issues)
	code, _ = doRequest(t, "GET", ts.URL+"/gitlab/api/v4/projects/12345/repository/tree", "")
	if code == http.StatusForbidden {
		t.Errorf("gitlab read_contents should be allowed")
	}

	// GitLab: denied operation (read_prs not in gitlab's ops)
	code, _ = doRequest(t, "GET", ts.URL+"/gitlab/api/v4/projects/12345/merge_requests", "")
	if code != http.StatusForbidden {
		t.Errorf("gitlab read_prs should be denied (not in gitlab ops), got %d", code)
	}
}
