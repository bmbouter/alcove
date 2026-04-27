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
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bmbouter/alcove/internal"
)

// --------------------------------------------------------------------
// Test helpers
// --------------------------------------------------------------------

// newTestProxy creates a Gate proxy configured with the given scope and
// credentials. It returns the proxy and a test HTTP server bound to it.
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

// newTestProxyWithMITM creates a Gate proxy with MITM enabled.
func newTestProxyWithMITM(t *testing.T, scope internal.Scope, creds map[string]string) (*Proxy, *httptest.Server, []byte) {
	t.Helper()
	certPEM, keyPEM, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("generating test CA: %v", err)
	}
	cfg := Config{
		SessionID:    "test-session",
		Scope:        scope,
		Credentials:  creds,
		ToolConfigs:  map[string]ToolConfig{},
		SessionToken: "session-tok",
		LLMToken:     "llm-secret-key",
		LLMProvider:  "anthropic",
		LLMTokenType: "api_key",
		CACertPEM:    certPEM,
		CAKeyPEM:     keyPEM,
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })
	return p, ts, certPEM
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

// doMITMRequest sends a request through the MITM proxy via CONNECT tunnel.
// Returns the HTTP response status code, response body, and any error.
func doMITMRequest(t *testing.T, proxyAddr string, caCertPEM []byte, method, targetHost, path string) (int, string) {
	t.Helper()

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", targetHost, targetHost)

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from CONNECT, got %d", resp.StatusCode)
	}

	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCertPEM)

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: targetHost,
		RootCAs:    caPool,
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake: %v", err)
	}

	req, _ := http.NewRequest(method, "https://"+targetHost+path, nil)
	if err := req.Write(tlsConn); err != nil {
		t.Fatalf("writing request: %v", err)
	}

	mitmReader := bufio.NewReader(tlsConn)
	mitmResp, err := http.ReadResponse(mitmReader, req)
	if err != nil {
		t.Fatalf("reading MITM response: %v", err)
	}
	defer mitmResp.Body.Close()

	body, _ := io.ReadAll(mitmResp.Body)
	return mitmResp.StatusCode, string(body)
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — GitHub (via CheckAccess)
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

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"read PRs", "GET", "https://api.github.com/repos/pulp/pulpcore/pulls"},
		{"read single PR", "GET", "https://api.github.com/repos/pulp/pulpcore/pulls/42"},
		{"create draft PR", "POST", "https://api.github.com/repos/pulp/pulpcore/pulls"},
		{"read file contents", "GET", "https://api.github.com/repos/pulp/pulpcore/contents/README.md"},
		{"read nested contents", "GET", "https://api.github.com/repos/pulp/pulpcore/contents/src/main.go"},
		{"read issues", "GET", "https://api.github.com/repos/pulp/pulpcore/issues"},
		{"read single issue", "GET", "https://api.github.com/repos/pulp/pulpcore/issues/7"},
		{"read commits", "GET", "https://api.github.com/repos/pulp/pulpcore/commits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed but got denied: %s", result.Reason)
			}
		})
	}
}

func TestGitHubScopeEnforcement_DeniedOperations(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs", "create_pr_draft", "read_contents"},
	)

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"merge PR - op not in scope", "PUT", "https://api.github.com/repos/pulp/pulpcore/pulls/1/merge"},
		{"delete branch - op not in scope", "DELETE", "https://api.github.com/repos/pulp/pulpcore/git/refs/heads/my-branch"},
		{"create issue - op not in scope", "POST", "https://api.github.com/repos/pulp/pulpcore/issues"},
		{"update PR - op not in scope", "PATCH", "https://api.github.com/repos/pulp/pulpcore/pulls/5"},
		{"write contents - op not in scope", "PUT", "https://api.github.com/repos/pulp/pulpcore/contents/file.txt"},
		{"create review - op not in scope", "POST", "https://api.github.com/repos/pulp/pulpcore/pulls/3/reviews"},
		{"create comment - op not in scope", "POST", "https://api.github.com/repos/pulp/pulpcore/issues/1/comments"},
		{"write actions - op not in scope", "POST", "https://api.github.com/repos/pulp/pulpcore/actions/workflows/ci.yml/dispatches"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

func TestGitHubScopeEnforcement_RepoNotInScope(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read_prs", "read_contents", "read_issues", "create_pr_draft"},
	)

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"read PRs - wrong repo", "GET", "https://api.github.com/repos/other/repo/pulls"},
		{"read contents - wrong repo", "GET", "https://api.github.com/repos/other/repo/contents/README.md"},
		{"create PR - wrong repo", "POST", "https://api.github.com/repos/other/repo/pulls"},
		{"read issues - wrong repo", "GET", "https://api.github.com/repos/evil/exfiltrate/issues"},
		{"read PRs - wrong org", "GET", "https://api.github.com/repos/notpulp/pulpcore/pulls"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

func TestGitHubScopeEnforcement_OrgWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/*"},
		[]string{"read_prs", "read_contents"},
	)

	result := CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if !result.Allowed {
		t.Errorf("expected allowed for pulp/pulpcore with pulp/* wildcard")
	}

	result = CheckAccess("GET", "https://api.github.com/repos/pulp/other-repo/pulls", scope)
	if !result.Allowed {
		t.Errorf("expected allowed for pulp/other-repo with pulp/* wildcard")
	}

	result = CheckAccess("GET", "https://api.github.com/repos/evil/repo/pulls", scope)
	if result.Allowed {
		t.Errorf("expected denied for evil/repo with pulp/* wildcard")
	}
}

func TestGitHubScopeEnforcement_RepoWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"*"},
		[]string{"read_prs"},
	)

	result := CheckAccess("GET", "https://api.github.com/repos/any/repo/pulls", scope)
	if !result.Allowed {
		t.Errorf("expected allowed for any/repo with * wildcard")
	}

	result = CheckAccess("PUT", "https://api.github.com/repos/any/repo/pulls/1/merge", scope)
	if result.Allowed {
		t.Errorf("expected denied for merge_pr not in scope")
	}
}

func TestGitHubScopeEnforcement_OperationWildcard(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"*"},
	)

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"read PRs", "GET", "https://api.github.com/repos/pulp/pulpcore/pulls"},
		{"merge PR", "PUT", "https://api.github.com/repos/pulp/pulpcore/pulls/1/merge"},
		{"delete branch", "DELETE", "https://api.github.com/repos/pulp/pulpcore/git/refs/heads/branch"},
		{"create issue", "POST", "https://api.github.com/repos/pulp/pulpcore/issues"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed with * operation wildcard: %s", result.Reason)
			}
		})
	}

	// Still denied on wrong repo
	result := CheckAccess("GET", "https://api.github.com/repos/evil/repo/pulls", scope)
	if result.Allowed {
		t.Errorf("expected denied for wrong repo even with * operations")
	}
}

func TestGitHubScopeEnforcement_ServiceNotInScope(t *testing.T) {
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{
			"gitlab": {Repos: []string{"*"}, Operations: []string{"*"}},
		},
	}

	result := CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if result.Allowed {
		t.Errorf("expected denied when github not in scope")
	}
}

func TestGitHubScopeEnforcement_EmptyRepoList(t *testing.T) {
	scope := githubScope([]string{}, []string{"read_prs", "read_contents"})

	result := CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if result.Allowed {
		t.Errorf("expected denied with empty repo list")
	}
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — GitLab (via CheckAccess)
// --------------------------------------------------------------------

func gitlabScope(repos, ops []string) internal.Scope {
	return internal.Scope{
		Services: map[string]internal.ServiceScope{
			"gitlab": {Repos: repos, Operations: ops},
		},
	}
}

func TestGitLabScopeEnforcement_AllowedOperations(t *testing.T) {
	scope := gitlabScope(
		[]string{"12345"},
		[]string{"read_prs", "create_pr_draft", "read_contents"},
	)

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"read MRs", "GET", "https://gitlab.com/api/v4/projects/12345/merge_requests"},
		{"read single MR", "GET", "https://gitlab.com/api/v4/projects/12345/merge_requests/1"},
		{"create MR", "POST", "https://gitlab.com/api/v4/projects/12345/merge_requests"},
		{"read repo tree", "GET", "https://gitlab.com/api/v4/projects/12345/repository/tree"},
		{"read repo file", "GET", "https://gitlab.com/api/v4/projects/12345/repository/files/README.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed but got denied: %s", result.Reason)
			}
		})
	}
}

func TestGitLabScopeEnforcement_DeniedOperations(t *testing.T) {
	scope := gitlabScope(
		[]string{"12345"},
		[]string{"read_prs", "read_contents"},
	)

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"merge MR - not in scope", "PUT", "https://gitlab.com/api/v4/projects/12345/merge_requests/1/merge"},
		{"create MR - not in scope", "POST", "https://gitlab.com/api/v4/projects/12345/merge_requests"},
		{"delete branch - not in scope", "DELETE", "https://gitlab.com/api/v4/projects/12345/repository/branches/feature"},
		{"write file - not in scope", "POST", "https://gitlab.com/api/v4/projects/12345/repository/files/new.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

func TestGitLabScopeEnforcement_ProjectNotInScope(t *testing.T) {
	scope := gitlabScope(
		[]string{"mygroup/myproject"},
		[]string{"read_prs", "read_contents", "create_pr_draft"},
	)

	tests := []struct {
		name string
		url  string
	}{
		{"wrong project (encoded)", "https://gitlab.com/api/v4/projects/evil%2Fproject/merge_requests"},
		{"wrong project (numeric)", "https://gitlab.com/api/v4/projects/99999/merge_requests"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess("GET", tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

// --------------------------------------------------------------------
// Category 3: Credential isolation
// --------------------------------------------------------------------

func TestCredentialInjection_BearerFormat(t *testing.T) {
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
	p := &Proxy{config: Config{
		Credentials: map[string]string{},
	}}

	req := httptest.NewRequest("GET", "https://api.github.com/repos/org/repo", nil)
	req.Header.Set("Authorization", "Bearer original")

	p.injectToolCredential(req, "github", "Authorization", "bearer")

	got := req.Header.Get("Authorization")
	if got != "Bearer original" {
		t.Errorf("expected original header unchanged, got %q", got)
	}
}

func TestCredentialInjection_LegacyFallback_GitHub(t *testing.T) {
	p := &Proxy{config: Config{
		Credentials: map[string]string{"github": "ghp_secret_123"},
		ToolConfigs: map[string]ToolConfig{},
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

	result := CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if !result.Allowed {
		t.Errorf("expected case-insensitive repo match to succeed")
	}
}

func TestScopeEnforcement_NonRepoGitHubEndpoint(t *testing.T) {
	scope := githubScope(
		[]string{"pulp/pulpcore"},
		[]string{"read"},
	)

	result := CheckAccess("GET", "https://api.github.com/user", scope)
	if !result.Allowed {
		t.Errorf("expected /user GET with 'read' op to be allowed")
	}

	result = CheckAccess("POST", "https://api.github.com/user", scope)
	if result.Allowed {
		t.Errorf("expected /user POST without 'write' op to be denied")
	}
}

func TestScopeEnforcement_HealthzAlwaysAllowed(t *testing.T) {
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

func TestProxyLog_DeniedConnectLogged(t *testing.T) {
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	p, ts := newTestProxy(t, scope, map[string]string{})

	// CONNECT to an unknown host should be denied and logged
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT unknown.example.com:443 HTTP/1.1\r\nHost: unknown.example.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unknown host CONNECT, got %d", resp.StatusCode)
	}

	entries := p.FlushLogs()
	if len(entries) == 0 {
		t.Fatal("expected at least one log entry for denied CONNECT")
	}
	found := false
	for _, e := range entries {
		if e.Decision == "deny" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log entry with decision=deny; got: %+v", entries)
	}
}

// --------------------------------------------------------------------
// Category 1: Scope enforcement — operation mapping exhaustive tests
// --------------------------------------------------------------------

func TestGitHubOperationMapping(t *testing.T) {
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
		{"GET", "labels", "read_issues"},
		{"POST", "labels", "write"},
		{"POST", "issues/42/labels", "update_issue"},
		{"DELETE", "issues/42/labels/bug", "update_issue"},
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

	// GitHub: allowed operation
	result := CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if !result.Allowed {
		t.Errorf("github read_prs should be allowed")
	}

	// GitHub: denied operation (read_contents not in github's ops)
	result = CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/contents/README.md", scope)
	if result.Allowed {
		t.Errorf("github read_contents should be denied (not in github ops)")
	}

	// GitLab: allowed operation
	result = CheckAccess("GET", "https://gitlab.com/api/v4/projects/12345/repository/tree", scope)
	if !result.Allowed {
		t.Errorf("gitlab read_contents should be allowed")
	}

	// GitLab: denied operation
	result = CheckAccess("GET", "https://gitlab.com/api/v4/projects/12345/merge_requests", scope)
	if result.Allowed {
		t.Errorf("gitlab read_prs should be denied (not in gitlab ops)")
	}
}

// --------------------------------------------------------------------
// JIRA / Atlassian tests
// --------------------------------------------------------------------

func jiraScope(ops []string) internal.Scope {
	return internal.Scope{
		Services: map[string]internal.ServiceScope{
			"jira": {Repos: []string{"*"}, Operations: ops},
		},
	}
}

func TestJiraOperationMapping(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		expected string
	}{
		{"GET", "rest/api/3/issue/PROJ-123", "read_issues"},
		{"GET", "rest/api/3/search", "search_issues"},
		{"POST", "rest/api/3/search/jql", "search_issues"},
		{"GET", "rest/api/3/issue/PROJ-123/comment", "read_comments"},
		{"GET", "rest/api/3/project", "read_projects"},
		{"GET", "rest/api/3/project/PROJ", "read_projects"},
		{"POST", "rest/api/3/issue", "create_issue"},
		{"PUT", "rest/api/3/issue/PROJ-123", "update_issue"},
		{"POST", "rest/api/3/issue/PROJ-123/comment", "add_comment"},
		{"PUT", "rest/api/3/issue/PROJ-123/comment/12345", "update_comment"},
		{"POST", "rest/api/3/issue/PROJ-123/transitions", "transition_issue"},
		{"PUT", "rest/api/3/issue/PROJ-123/assignee", "assign_issue"},
		{"DELETE", "rest/api/3/issue/PROJ-123", "delete_issue"},
		{"DELETE", "rest/api/3/issue/PROJ-123/comment/456", "delete_comment"},
		{"GET", "rest/api/3/issuetype", "read_metadata"},
		{"GET", "rest/agile/1.0/board", "read_boards"},
		{"GET", "rest/agile/1.0/sprint/5", "read_sprints"},
		{"POST", "rest/agile/1.0/sprint/5/issue", "move_to_sprint"},
		{"GET", "rest/api/3/issue/PROJ-123/transitions", "read_transitions"},
		{"POST", "rest/api/3/issue/PROJ-123/worklog", "add_worklog"},
	}

	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.path, func(t *testing.T) {
			got := mapJiraOperation(tt.method, tt.path)
			if got != tt.expected {
				t.Errorf("mapJiraOperation(%q, %q) = %q, want %q", tt.method, tt.path, got, tt.expected)
			}
		})
	}
}

func TestJiraScopeEnforcement_TotalBlocking(t *testing.T) {
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{},
	}

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"GET issue", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"POST issue", "POST", "https://company.atlassian.net/rest/api/3/issue"},
		{"GET search", "GET", "https://company.atlassian.net/rest/api/3/search?jql=project=PROJ"},
		{"GET project", "GET", "https://company.atlassian.net/rest/api/3/project"},
		{"PUT issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"DELETE issue", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"GET boards", "GET", "https://company.atlassian.net/rest/agile/1.0/board"},
		{"POST comment", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied (no jira in scope) but got allowed")
			}
		})
	}
}

func TestJiraScopeEnforcement_ReadOnly(t *testing.T) {
	scope := jiraScope([]string{
		"read_issues", "search_issues", "read_projects",
		"read_comments", "read_metadata", "read_boards",
		"read_sprints", "read_transitions",
	})

	readTests := []struct {
		name   string
		method string
		url    string
	}{
		{"read issue", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"search issues", "GET", "https://company.atlassian.net/rest/api/3/search?jql=project=PROJ"},
		{"read project", "GET", "https://company.atlassian.net/rest/api/3/project"},
		{"read comments", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
		{"read boards", "GET", "https://company.atlassian.net/rest/agile/1.0/board"},
		{"read sprints", "GET", "https://company.atlassian.net/rest/agile/1.0/sprint/5"},
		{"read transitions", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/transitions"},
	}

	for _, tt := range readTests {
		t.Run("allowed_"+tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed but got denied: %s", result.Reason)
			}
		})
	}

	writeTests := []struct {
		name   string
		method string
		url    string
	}{
		{"create issue", "POST", "https://company.atlassian.net/rest/api/3/issue"},
		{"update issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"delete issue", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"add comment", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
		{"transition issue", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/transitions"},
		{"assign issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/assignee"},
	}

	for _, tt := range writeTests {
		t.Run("denied_"+tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

func TestJiraScopeEnforcement_FullAccess(t *testing.T) {
	scope := jiraScope([]string{"*"})

	tests := []struct {
		name   string
		method string
		url    string
	}{
		{"read issue", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"search issues", "GET", "https://company.atlassian.net/rest/api/3/search?jql=project=PROJ"},
		{"read project", "GET", "https://company.atlassian.net/rest/api/3/project"},
		{"create issue", "POST", "https://company.atlassian.net/rest/api/3/issue"},
		{"update issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"delete issue", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"add comment", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
		{"update comment", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment/12345"},
		{"delete comment", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment/456"},
		{"transition issue", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/transitions"},
		{"assign issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/assignee"},
		{"read boards", "GET", "https://company.atlassian.net/rest/agile/1.0/board"},
		{"read sprints", "GET", "https://company.atlassian.net/rest/agile/1.0/sprint/5"},
		{"move to sprint", "POST", "https://company.atlassian.net/rest/agile/1.0/sprint/5/issue"},
		{"add worklog", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/worklog"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed with * wildcard: %s", result.Reason)
			}
		})
	}
}

func TestJiraScopeEnforcement_ReducedWrite(t *testing.T) {
	scope := jiraScope([]string{
		"read_issues", "search_issues", "read_projects", "read_comments",
		"create_issue", "add_comment",
	})

	allowedTests := []struct {
		name   string
		method string
		url    string
	}{
		{"read issue", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"search issues", "GET", "https://company.atlassian.net/rest/api/3/search?jql=project=PROJ"},
		{"read project", "GET", "https://company.atlassian.net/rest/api/3/project"},
		{"read comments", "GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
		{"create issue", "POST", "https://company.atlassian.net/rest/api/3/issue"},
		{"add comment", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment"},
	}

	for _, tt := range allowedTests {
		t.Run("allowed_"+tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if !result.Allowed {
				t.Errorf("expected allowed but got denied: %s", result.Reason)
			}
		})
	}

	deniedTests := []struct {
		name   string
		method string
		url    string
	}{
		{"delete issue", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"update issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123"},
		{"transition issue", "POST", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/transitions"},
		{"assign issue", "PUT", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/assignee"},
		{"delete comment", "DELETE", "https://company.atlassian.net/rest/api/3/issue/PROJ-123/comment/456"},
	}

	for _, tt := range deniedTests {
		t.Run("denied_"+tt.name, func(t *testing.T) {
			result := CheckAccess(tt.method, tt.url, scope)
			if result.Allowed {
				t.Errorf("expected denied but got allowed")
			}
		})
	}
}

func TestJiraCredentialInjection_Basic(t *testing.T) {
	p := &Proxy{config: Config{
		Credentials: map[string]string{"jira": "user@example.com:api-token-123"},
	}}

	req := httptest.NewRequest("GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-1", nil)
	p.injectToolCredential(req, "jira", "Authorization", "basic")

	got := req.Header.Get("Authorization")
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user@example.com:api-token-123"))
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestJiraCredentialIsolation(t *testing.T) {
	// Scope only allows jira, NOT github or gitlab
	scope := internal.Scope{
		Services: map[string]internal.ServiceScope{
			"jira": {Repos: []string{"*"}, Operations: []string{"*"}},
		},
	}

	// JIRA request should be allowed
	result := CheckAccess("GET", "https://company.atlassian.net/rest/api/3/issue/PROJ-123", scope)
	if !result.Allowed {
		t.Errorf("expected jira request to be allowed")
	}

	// GitHub request should be denied
	result = CheckAccess("GET", "https://api.github.com/repos/pulp/pulpcore/pulls", scope)
	if result.Allowed {
		t.Errorf("expected github to be denied (not in scope)")
	}

	// GitLab request should be denied
	result = CheckAccess("GET", "https://gitlab.com/api/v4/projects/12345/merge_requests", scope)
	if result.Allowed {
		t.Errorf("expected gitlab to be denied (not in scope)")
	}
}

// --------------------------------------------------------------------
// MITM CONNECT integration tests
// --------------------------------------------------------------------

func TestCONNECT_MITMEnabled_GitHubAllowed(t *testing.T) {
	scope := githubScope([]string{"*"}, []string{"*"})
	_, ts, certPEM := newTestProxyWithMITM(t, scope, map[string]string{"github": "ghp_real_token"})

	// CONNECT to api.github.com should succeed (200) with MITM enabled
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from CONNECT with MITM, got %d", resp.StatusCode)
	}

	// TLS handshake should succeed with our CA
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(certPEM)
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    caPool,
		NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake failed: %v", err)
	}
	tlsConn.Close()
}

func TestCONNECT_UnknownHostDenied(t *testing.T) {
	scope := githubScope([]string{"*"}, []string{"*"})
	_, ts := newTestProxy(t, scope, map[string]string{"github": "ghp_token"})

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unknown host CONNECT, got %d", resp.StatusCode)
	}
}

func TestCONNECT_AllowlistDomainPassthrough(t *testing.T) {
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	_, ts := newTestProxy(t, scope, map[string]string{})

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	// pypi.org is on the domain allowlist
	fmt.Fprintf(conn, "CONNECT pypi.org:443 HTTP/1.1\r\nHost: pypi.org:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	// Should succeed (200) — passthrough tunnel
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for allowlisted domain CONNECT, got %d", resp.StatusCode)
	}
}

// --------------------------------------------------------------------
// LLM OAuth token injection tests
// --------------------------------------------------------------------

func TestLLMOAuthToken_InjectsCorrectHeaders(t *testing.T) {
	cfg := Config{
		SessionID:    "test-session",
		Scope:        internal.Scope{Services: map[string]internal.ServiceScope{}},
		Credentials:  map[string]string{},
		ToolConfigs:  map[string]ToolConfig{},
		SessionToken: "session-tok",
		LLMToken:     "sk-ant-oat01-test",
		LLMProvider:  "anthropic",
		LLMTokenType: "oauth_token",
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })

	code, body := doRequest(t, "POST", ts.URL+"/v1/messages", `{"model":"claude-sonnet-4-20250514","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	if code == http.StatusInternalServerError && strings.Contains(body, "unknown LLM provider") {
		t.Errorf("oauth_token type should be accepted, but got 'unknown LLM provider' error")
	}
}

func TestLLMOAuthToken_NotUnknownProvider(t *testing.T) {
	cfg := Config{
		SessionID:    "test-session",
		Scope:        internal.Scope{Services: map[string]internal.ServiceScope{}},
		Credentials:  map[string]string{},
		ToolConfigs:  map[string]ToolConfig{},
		SessionToken: "session-tok",
		LLMToken:     "sk-ant-oat01-test",
		LLMProvider:  "anthropic",
		LLMTokenType: "oauth_token",
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })

	code, body := doRequest(t, "POST", ts.URL+"/v1/messages", `{"model":"claude-sonnet-4-20250514","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	if code == http.StatusInternalServerError && strings.Contains(body, "unknown LLM provider") {
		t.Errorf("oauth_token with anthropic provider should not trigger 'unknown LLM provider'; got %d: %s", code, body)
	}
}

// --------------------------------------------------------------------
// Monitor mode tests — CONNECT routing level
// --------------------------------------------------------------------

// newTestProxyWithMonitor creates a Gate proxy in monitor enforcement mode.
func newTestProxyWithMonitor(t *testing.T, scope internal.Scope, creds map[string]string) (*Proxy, *httptest.Server) {
	t.Helper()
	cfg := Config{
		SessionID:       "test-session",
		Scope:           scope,
		Credentials:     creds,
		ToolConfigs:     map[string]ToolConfig{},
		SessionToken:    "session-tok",
		LLMToken:        "llm-secret-key",
		LLMProvider:     "anthropic",
		LLMTokenType:    "api_key",
		EnforcementMode: "monitor",
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })
	return p, ts
}

func TestCONNECT_MonitorMode_AllowsUnknownHost(t *testing.T) {
	// Start a local TCP listener to act as the "unknown" upstream so the
	// tunnel dial succeeds in the test environment.
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting upstream listener: %v", err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	p, ts := newTestProxyWithMonitor(t, scope, map[string]string{})

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	// Send CONNECT to 127.0.0.1:<port> — not an LLM, service, or allowlisted host,
	// so it hits the default case. Monitor mode should allow it (200).
	target := upstream.Addr().String()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("monitor mode should allow unknown host CONNECT (200), got %d", resp.StatusCode)
	}

	// Close our side so bidirectionalCopy in tunnelDirect finishes and the
	// log entry is written.
	conn.Close()

	// Wait briefly for the server goroutine to finish and record the log entry.
	var entries []internal.ProxyLogEntry
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		entries = p.FlushLogs()
		if len(entries) > 0 {
			break
		}
	}
	found := false
	for _, e := range entries {
		if e.Decision == "allow" && e.Operation == "monitor" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log entry with decision=allow, operation=monitor; got: %+v", entries)
	}
}

func TestCONNECT_EnforceMode_DeniesUnknownHost(t *testing.T) {
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	// Use the standard test proxy (enforce mode by default)
	p, ts := newTestProxy(t, scope, map[string]string{})

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	// Send CONNECT to an unknown host — enforce mode should deny (403)
	fmt.Fprintf(conn, "CONNECT unknown.example.com:443 HTTP/1.1\r\nHost: unknown.example.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("enforce mode should deny unknown host CONNECT (403), got %d", resp.StatusCode)
	}

	// Verify audit log records deny
	entries := p.FlushLogs()
	found := false
	for _, e := range entries {
		if e.Decision == "deny" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log entry with decision=deny; got: %+v", entries)
	}
}

func TestProxyRequest_MonitorMode_AllowsUnknownHost(t *testing.T) {
	// Set up a backend server to receive the forwarded request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "forwarded-ok")
	}))
	t.Cleanup(func() { backend.Close() })

	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	_, ts := newTestProxyWithMonitor(t, scope, map[string]string{})

	// Use the Gate proxy to forward a plain HTTP request to the backend.
	proxyURL, _ := url.Parse(ts.URL)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}

	resp, err := client.Get(backend.URL + "/test")
	if err != nil {
		t.Fatalf("sending request through proxy: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("monitor mode should allow unknown host proxy request, got 403: %s", string(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}
	if string(body) != "forwarded-ok" {
		t.Errorf("expected forwarded-ok body, got %q", string(body))
	}
}

// newTestProxyWithMonitorAndMITM creates a Gate proxy in monitor mode with MITM enabled.
func newTestProxyWithMonitorAndMITM(t *testing.T, scope internal.Scope, creds map[string]string) (*Proxy, *httptest.Server, []byte) {
	t.Helper()
	certPEM, keyPEM, err := GenerateTestCA()
	if err != nil {
		t.Fatalf("generating test CA: %v", err)
	}
	cfg := Config{
		SessionID:       "test-session",
		Scope:           scope,
		Credentials:     creds,
		ToolConfigs:     map[string]ToolConfig{},
		SessionToken:    "session-tok",
		LLMToken:        "llm-secret-key",
		LLMProvider:     "anthropic",
		LLMTokenType:    "api_key",
		CACertPEM:       certPEM,
		CAKeyPEM:        keyPEM,
		EnforcementMode: "monitor",
	}
	p := NewProxy(cfg)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Stop() })
	return p, ts, certPEM
}

func TestCONNECT_MonitorMode_MITMsServiceHost(t *testing.T) {
	// In monitor mode with MITM enabled, a CONNECT to api.github.com should
	// go through the MITM handler (TLS interception for credential injection)
	// rather than tunnelDirect (raw passthrough).
	scope := githubScope([]string{"*"}, []string{"*"})
	_, ts, certPEM := newTestProxyWithMonitorAndMITM(t, scope, map[string]string{"github": "ghp_real_token"})

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatalf("connecting to gate: %v", err)
	}
	defer conn.Close()

	// Send CONNECT to api.github.com — this is a service domain that should be MITM'd
	fmt.Fprintf(conn, "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("reading CONNECT response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from CONNECT in monitor+MITM mode, got %d", resp.StatusCode)
	}

	// If MITM is active, the TLS handshake should succeed with our test CA cert.
	// If it fell through to tunnelDirect, the handshake would fail because the
	// upstream is the real api.github.com and our CA cert is not in its chain.
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(certPEM)
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: "api.github.com",
		RootCAs:    caPool,
		NextProtos: []string{"http/1.1"},
	})
	err = tlsConn.Handshake()
	if err != nil {
		t.Fatalf("TLS handshake failed — MITM not active in monitor mode for service domain: %v", err)
	}
	tlsConn.Close()
}
