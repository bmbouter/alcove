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

package bridge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test the getStringSliceInput helper function
func TestGetStringSliceInput(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		key      string
		expected []string
	}{
		{
			name:     "string slice",
			inputs:   map[string]interface{}{"labels": []string{"bug", "urgent"}},
			key:      "labels",
			expected: []string{"bug", "urgent"},
		},
		{
			name:     "interface slice",
			inputs:   map[string]interface{}{"labels": []interface{}{"bug", "urgent"}},
			key:      "labels",
			expected: []string{"bug", "urgent"},
		},
		{
			name:     "missing key",
			inputs:   map[string]interface{}{},
			key:      "labels",
			expected: nil,
		},
		{
			name:     "wrong type",
			inputs:   map[string]interface{}{"labels": "not-an-array"},
			key:      "labels",
			expected: nil,
		},
		{
			name:     "mixed types in slice",
			inputs:   map[string]interface{}{"labels": []interface{}{"bug", 123, "urgent"}},
			key:      "labels",
			expected: []string{"bug", "urgent"}, // non-strings filtered out
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStringSliceInput(tt.inputs, tt.key)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, result)
				return
			}
			for i, v := range result {
				if v != tt.expected[i] {
					t.Errorf("expected %v, got %v", tt.expected, result)
					break
				}
			}
		})
	}
}

// Test GitHub update issue action
func TestBridgeActionUpdateGHIssue(t *testing.T) {
	// Mock GitHub API server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/labels"):
			// Add labels
			w.WriteHeader(200)
			w.Write([]byte(`[{"name": "bug"}, {"name": "urgent"}]`))
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/labels/"):
			// Remove label
			w.WriteHeader(200)
		case r.Method == "POST" && strings.Contains(r.URL.Path, "/assignees"):
			// Add assignees
			w.WriteHeader(200)
			w.Write([]byte(`{"assignees": [{"login": "testuser"}]}`))
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/assignees"):
			// Remove assignees
			w.WriteHeader(200)
		case r.Method == "PATCH" && strings.Contains(r.URL.Path, "/issues/"):
			// Update state
			w.WriteHeader(200)
			w.Write([]byte(`{"state": "closed"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	// Test input
	inputs := map[string]interface{}{
		"repo":             "owner/repo",
		"issue":            123,
		"add_labels":       []string{"bug", "urgent"},
		"remove_labels":    []string{"wontfix"},
		"add_assignees":    []string{"testuser"},
		"remove_assignees": []string{"olduser"},
		"state":            "closed",
	}

	ctx := context.Background()

	// Create a simple test credential store that returns the test server URL
	testCredStore := &testCredStore{apiHost: server.URL}

	result, err := bridgeActionUpdateGHIssue(ctx, inputs, testCredStore, "test-team")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("expected status 'succeeded', got: %s", result.Status)
	}

	if updated, ok := result.Outputs["updated"].(bool); !ok || !updated {
		t.Errorf("expected outputs.updated to be true, got: %v", result.Outputs["updated"])
	}
}

// Test unified update issue action auto-detection
func TestBridgeActionUnifiedUpdateIssue(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		expected string // expected error message, empty for success
	}{
		{
			name:   "GitHub detected",
			inputs: map[string]interface{}{"repo": "owner/repo", "issue": 123},
		},
		{
			name:   "GitLab detected",
			inputs: map[string]interface{}{"project": "group/project", "issue": 123},
		},
		{
			name:     "ambiguous inputs",
			inputs:   map[string]interface{}{"issue": 123},
			expected: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			credStore := &testCredStore{token: "fake", apiHost: "http://fake"}

			result, err := bridgeActionUnifiedUpdateIssue(ctx, tt.inputs, credStore, "test-team")
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}

			if tt.expected != "" {
				if result.Status != "failed" || result.Error != tt.expected {
					t.Errorf("expected error %q, got status=%s error=%q", tt.expected, result.Status, result.Error)
				}
			}
		})
	}
}

// Test that all new actions are registered
func TestNewActionsRegistered(t *testing.T) {
	actions := RegisterBridgeActions()

	expectedActions := []string{
		"update-issue",
		"update-gh-issue",
		"update-gl-issue",
	}

	for _, actionName := range expectedActions {
		if _, exists := actions[actionName]; !exists {
			t.Errorf("action %q not registered", actionName)
		}
	}
}

// Test that schemas are present for new actions
func TestNewActionSchemas(t *testing.T) {
	schemas := ListBridgeActionSchemas()

	found := false
	for _, schema := range schemas {
		if schema.Name == "update-issue" {
			found = true

			// Check required inputs are documented
			if _, ok := schema.Inputs["issue"]; !ok {
				t.Error("update-issue schema missing 'issue' input")
			}
			if _, ok := schema.Inputs["add_labels"]; !ok {
				t.Error("update-issue schema missing 'add_labels' input")
			}
			if _, ok := schema.Outputs["updated"]; !ok {
				t.Error("update-issue schema missing 'updated' output")
			}
			break
		}
	}

	if !found {
		t.Error("update-issue action schema not found")
	}
}

// Simple test credential store for mocking
type testCredStore struct {
	token   string
	apiHost string
}

func (t *testCredStore) AcquireSCMTokenForOwner(ctx context.Context, provider, teamID string) (string, string, error) {
	if t.token == "" {
		return "fake-token", t.apiHost, nil
	}
	return t.token, t.apiHost, nil
}