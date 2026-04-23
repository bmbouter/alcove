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
	"testing"

	"github.com/bmbouter/alcove/internal"
)

func TestMatchPathGlob(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		pattern string
		want    bool
	}{
		// Exact paths
		{"exact match", "/repos/owner/repo/issues", "/repos/owner/repo/issues", true},
		{"exact mismatch", "/repos/owner/repo/issues", "/repos/owner/repo/pulls", false},
		{"root path", "/", "/", true},
		{"exact with trailing slash", "/repos/", "/repos/", true},

		// Single-segment wildcard (*)
		{"star matches one segment", "/repos/owner/repo/issues", "/repos/*/repo/issues", true},
		{"star matches owner and repo", "/repos/owner/repo/issues", "/repos/*/*/issues", true},
		{"star does not match zero segments", "/repos/issues", "/repos/*/issues", false},
		{"star does not match multiple segments", "/repos/owner/sub/repo/issues", "/repos/*/repo/issues", false},
		{"star at end matches one segment", "/repos/owner", "/repos/*", true},
		{"star at end does not match two segments", "/repos/owner/repo", "/repos/*", false},

		// Multi-segment wildcard (**)
		{"doublestar matches zero segments", "/repos/issues", "/repos/**/issues", true},
		{"doublestar matches one segment", "/repos/owner/issues", "/repos/**/issues", true},
		{"doublestar matches multiple segments", "/repos/owner/repo/sub/issues", "/repos/**/issues", true},
		{"doublestar at end matches everything", "/repos/owner/repo/anything", "/repos/**", true},
		{"doublestar at end matches zero", "/repos", "/repos/**", true},
		{"doublestar at start", "/a/b/c/end", "/**/end", true},
		{"doublestar matches entire path", "/any/path/here", "/**", true},

		// Edge cases
		{"empty path vs empty pattern", "", "", true},
		{"empty path vs non-empty pattern", "", "/repos", false},
		{"non-empty path vs empty pattern", "/repos", "", false},
		{"double slash in path", "/repos//issues", "/repos//issues", true},
		{"trailing slash pattern no trailing slash path", "/repos", "/repos/", false},

		// Combined wildcards
		{"star and doublestar", "/repos/owner/repo/pulls/123/reviews", "/repos/*/*/pulls/*/reviews", true},
		{"star and doublestar combined", "/repos/owner/repo/a/b/c", "/repos/*/*/**", true},
		{"doublestar then star", "/a/b/c/d", "/**/*/d", true},

		// Real GitHub API patterns
		{"github issues", "/repos/owner/repo/issues", "/repos/*/*/issues", true},
		{"github pulls reviews", "/repos/owner/repo/pulls/42/reviews", "/repos/*/*/pulls/*/reviews", true},
		{"github graphql", "/graphql", "/graphql", true},
		{"github contents deep", "/repos/owner/repo/contents/src/main.go", "/repos/*/*/contents/**", true},
		{"github contents root", "/repos/owner/repo/contents", "/repos/*/*/contents/**", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchPathGlob(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPathGlob(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchesRule(t *testing.T) {
	tests := []struct {
		name   string
		method string
		host   string
		path   string
		rule   internal.HTTPRule
		want   bool
	}{
		// Method matching
		{
			"exact method match",
			"GET", "api.github.com", "/repos/owner/repo",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/owner/repo"},
			true,
		},
		{
			"method wildcard",
			"POST", "api.github.com", "/repos/owner/repo",
			internal.HTTPRule{Method: "*", Host: "api.github.com", Path: "/repos/owner/repo"},
			true,
		},
		{
			"method case insensitive",
			"get", "api.github.com", "/repos/owner/repo",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/owner/repo"},
			true,
		},
		{
			"method mismatch",
			"POST", "api.github.com", "/repos/owner/repo",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/owner/repo"},
			false,
		},

		// Host matching
		{
			"host case insensitive",
			"GET", "API.GitHub.Com", "/repos/owner/repo",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/owner/repo"},
			true,
		},
		{
			"host mismatch",
			"GET", "api.gitlab.com", "/repos/owner/repo",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/owner/repo"},
			false,
		},

		// Path matching via glob
		{
			"path with glob",
			"GET", "api.github.com", "/repos/owner/repo/issues",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/*/*/issues"},
			true,
		},
		{
			"path glob mismatch",
			"GET", "api.github.com", "/repos/owner/repo/pulls",
			internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/*/*/issues"},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesRule(tt.method, tt.host, tt.path, tt.rule)
			if got != tt.want {
				t.Errorf("matchesRule(%q, %q, %q, %+v) = %v, want %v",
					tt.method, tt.host, tt.path, tt.rule, got, tt.want)
			}
		})
	}
}

func TestCheckPolicyRules(t *testing.T) {
	rules := []internal.PolicyRule{
		{Allow: internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/*/*/issues"}},
		{Allow: internal.HTTPRule{Method: "POST", Host: "api.github.com", Path: "/repos/*/*/issues"}},
		{Allow: internal.HTTPRule{Method: "*", Host: "api.github.com", Path: "/repos/*/*/pulls/*/reviews"}},
		{Allow: internal.HTTPRule{Method: "POST", Host: "api.github.com", Path: "/graphql"}},
		{Allow: internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/*/*/contents/**"}},
	}

	tests := []struct {
		name       string
		method     string
		url        string
		rules      []internal.PolicyRule
		wantAllow  bool
		wantReason string // substring check for denied requests
	}{
		// Matching rules
		{
			"GET issues allowed",
			"GET", "https://api.github.com/repos/owner/repo/issues",
			rules, true, "",
		},
		{
			"POST issues allowed",
			"POST", "https://api.github.com/repos/owner/repo/issues",
			rules, true, "",
		},
		{
			"any method on reviews allowed",
			"PUT", "https://api.github.com/repos/owner/repo/pulls/42/reviews",
			rules, true, "",
		},
		{
			"POST graphql allowed",
			"POST", "https://api.github.com/graphql",
			rules, true, "",
		},
		{
			"GET deep contents allowed",
			"GET", "https://api.github.com/repos/owner/repo/contents/src/main.go",
			rules, true, "",
		},
		{
			"GET root contents allowed",
			"GET", "https://api.github.com/repos/owner/repo/contents",
			rules, true, "",
		},

		// First match wins
		{
			"first matching rule wins",
			"GET", "https://api.github.com/repos/owner/repo/issues",
			rules, true, "",
		},

		// No match -> denied
		{
			"DELETE issues not allowed",
			"DELETE", "https://api.github.com/repos/owner/repo/issues",
			rules, false, "no policy rule allows",
		},
		{
			"wrong host denied",
			"GET", "https://api.gitlab.com/repos/owner/repo/issues",
			rules, false, "no policy rule allows",
		},
		{
			"wrong path denied",
			"GET", "https://api.github.com/repos/owner/repo/actions",
			rules, false, "no policy rule allows",
		},

		// Empty rules -> denied
		{
			"empty rules deny everything",
			"GET", "https://api.github.com/repos/owner/repo/issues",
			nil, false, "no policy rule allows",
		},
		{
			"empty rules slice denies",
			"GET", "https://api.github.com/anything",
			[]internal.PolicyRule{}, false, "no policy rule allows",
		},

		// Invalid URL
		{
			"invalid URL denied",
			"GET", "://invalid",
			rules, false, "invalid URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckPolicyRules(tt.method, tt.url, tt.rules)
			if result.Allowed != tt.wantAllow {
				t.Errorf("CheckPolicyRules(%q, %q) allowed=%v, want %v (reason: %s)",
					tt.method, tt.url, result.Allowed, tt.wantAllow, result.Reason)
			}
			if !tt.wantAllow && tt.wantReason != "" {
				if !contains(result.Reason, tt.wantReason) {
					t.Errorf("CheckPolicyRules(%q, %q) reason=%q, want to contain %q",
						tt.method, tt.url, result.Reason, tt.wantReason)
				}
			}
			if tt.wantAllow {
				if result.Service == "" {
					t.Errorf("CheckPolicyRules(%q, %q) service should not be empty on allow", tt.method, tt.url)
				}
				if result.Operation == "" {
					t.Errorf("CheckPolicyRules(%q, %q) operation should not be empty on allow", tt.method, tt.url)
				}
			}
		})
	}
}

func TestCheckPolicyRulesRealPatterns(t *testing.T) {
	// Test with real-world GitHub API patterns
	rules := []internal.PolicyRule{
		// Read-only access to repos
		{Allow: internal.HTTPRule{Method: "GET", Host: "api.github.com", Path: "/repos/**"}},
		// Create and read PRs
		{Allow: internal.HTTPRule{Method: "POST", Host: "api.github.com", Path: "/repos/*/*/pulls"}},
		// GraphQL
		{Allow: internal.HTTPRule{Method: "POST", Host: "api.github.com", Path: "/graphql"}},
	}

	tests := []struct {
		name      string
		method    string
		url       string
		wantAllow bool
	}{
		{"read repo", "GET", "https://api.github.com/repos/owner/repo", true},
		{"read issues", "GET", "https://api.github.com/repos/owner/repo/issues", true},
		{"read deep path", "GET", "https://api.github.com/repos/owner/repo/pulls/1/reviews/2", true},
		{"create PR", "POST", "https://api.github.com/repos/owner/repo/pulls", true},
		{"graphql query", "POST", "https://api.github.com/graphql", true},
		{"delete not allowed", "DELETE", "https://api.github.com/repos/owner/repo", false},
		{"POST issues not allowed", "POST", "https://api.github.com/repos/owner/repo/issues", false},
		{"user endpoint not allowed", "GET", "https://api.github.com/user", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckPolicyRules(tt.method, tt.url, rules)
			if result.Allowed != tt.wantAllow {
				t.Errorf("CheckPolicyRules(%q, %q) allowed=%v, want %v (reason: %s)",
					tt.method, tt.url, result.Allowed, tt.wantAllow, result.Reason)
			}
		})
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
