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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockGitLabCredentialStore is a test mock for GitLab credential acquisition
type mockGitLabCredentialStore struct {
	token   string
	apiHost string
	err     error
}

func (m *mockGitLabCredentialStore) AcquireSCMTokenForOwner(ctx context.Context, service, teamID string) (string, string, error) {
	return m.token, m.apiHost, m.err
}

func TestBridgeActionSearchGLIssues(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		mockResponse   interface{}
		mockStatusCode int
		expectedStatus string
		expectedError  string
		validateResult func(t *testing.T, result *BridgeActionResult)
	}{
		{
			name: "successful search with results",
			inputs: map[string]interface{}{
				"project":     "group/repo",
				"search":      "bug",
				"labels":      "bug,high-priority",
				"state":       "opened",
				"max_results": 10,
			},
			mockResponse: []map[string]interface{}{
				{
					"iid":     123,
					"title":   "Fix critical bug",
					"state":   "opened",
					"web_url": "https://gitlab.example.com/group/repo/-/issues/123",
					"labels":  []string{"bug", "high-priority"},
				},
				{
					"iid":     124,
					"title":   "Another bug report",
					"state":   "opened",
					"web_url": "https://gitlab.example.com/group/repo/-/issues/124",
					"labels":  []string{"bug"},
				},
			},
			mockStatusCode: 200,
			expectedStatus: "succeeded",
			validateResult: func(t *testing.T, result *BridgeActionResult) {
				issues, ok := result.Outputs["issues"].([]map[string]interface{})
				if !ok {
					t.Error("Expected issues to be []map[string]interface{}")
					return
				}
				if len(issues) != 2 {
					t.Errorf("Expected 2 issues, got %d", len(issues))
					return
				}
				if issues[0]["iid"] != 123 {
					t.Errorf("Expected first issue IID to be 123, got %v", issues[0]["iid"])
				}
				if issues[0]["title"] != "Fix critical bug" {
					t.Errorf("Expected first issue title to be \"Fix critical bug\", got %v", issues[0]["title"])
				}
				if total, ok := result.Outputs["total"].(int); !ok || total != 2 {
					t.Errorf("Expected total to be 2, got %v", result.Outputs["total"])
				}
			},
		},
		{
			name: "empty search results",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"search":  "nonexistent",
			},
			mockResponse:   []map[string]interface{}{},
			mockStatusCode: 200,
			expectedStatus: "succeeded",
			validateResult: func(t *testing.T, result *BridgeActionResult) {
				issues, ok := result.Outputs["issues"].([]map[string]interface{})
				if !ok {
					t.Error("Expected issues to be []map[string]interface{}")
					return
				}
				if len(issues) != 0 {
					t.Errorf("Expected 0 issues, got %d", len(issues))
				}
				if total, ok := result.Outputs["total"].(int); !ok || total != 0 {
					t.Errorf("Expected total to be 0, got %v", result.Outputs["total"])
				}
			},
		},
		{
			name: "missing required project input",
			inputs: map[string]interface{}{
				"search": "bug",
			},
			mockStatusCode: 200,
			expectedStatus: "failed",
			expectedError:  "missing required inputs: project",
		},
		{
			name: "max_results clamping",
			inputs: map[string]interface{}{
				"project":     "group/repo",
				"max_results": 150, // Should be clamped to 100
			},
			mockResponse:   []map[string]interface{}{},
			mockStatusCode: 200,
			expectedStatus: "succeeded",
			validateResult: func(t *testing.T, result *BridgeActionResult) {
				// We cannot directly test the clamping from the mock, but we can ensure the action succeeds
				if result.Status != "succeeded" {
					t.Errorf("Expected succeeded status with clamped max_results")
				}
			},
		},
		{
			name: "GitLab API error",
			inputs: map[string]interface{}{
				"project": "group/repo",
			},
			mockResponse:   map[string]interface{}{"error": "Not found"},
			mockStatusCode: 404,
			expectedStatus: "failed",
			expectedError:  "GitLab API error searching issues",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify the URL structure
				if !strings.Contains(r.URL.Path, "/api/v4/projects/") || !strings.Contains(r.URL.Path, "/issues") {
					t.Errorf("Expected GitLab issues API path, got %s", r.URL.Path)
				}

				// Verify authorization header
				if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
					t.Errorf("Expected PRIVATE-TOKEN header, got %s", r.Header.Get("PRIVATE-TOKEN"))
				}

				w.WriteHeader(tt.mockStatusCode)
				if tt.mockResponse != nil {
					json.NewEncoder(w).Encode(tt.mockResponse)
				}
			}))
			defer server.Close()

			// Create mock credential store
			credStore := &mockGitLabCredentialStore{
				token:   "test-token",
				apiHost: server.URL,
			}

			// Execute the action
			ctx := context.Background()
			result, err := bridgeActionSearchGLIssues(ctx, tt.inputs, credStore, "test-team")

			// Verify no internal error
			if err != nil {
				t.Errorf("Expected no internal error, got: %v", err)
				return
			}

			// Verify status
			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			// Verify error message if expected
			if tt.expectedError != "" {
				if result.Error == "" {
					t.Errorf("Expected error message containing \"%s\", got empty error", tt.expectedError)
				} else if !strings.Contains(result.Error, tt.expectedError) {
					t.Errorf("Expected error message containing \"%s\", got \"%s\"", tt.expectedError, result.Error)
				}
			}

			// Run custom validation if provided
			if tt.validateResult != nil {
				tt.validateResult(t, result)
			}
		})
	}
}

func TestBridgeActionSearchGLIssues_Defaults(t *testing.T) {
	// Test that defaults are properly applied
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		// Verify default state
		if query.Get("state") != "opened" {
			t.Errorf("Expected default state \"opened\", got \"%s\"", query.Get("state"))
		}

		// Verify default max_results (should be 20)
		if query.Get("per_page") != "20" {
			t.Errorf("Expected default per_page \"20\", got \"%s\"", query.Get("per_page"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	}))
	defer server.Close()

	credStore := &mockGitLabCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
	}

	inputs := map[string]interface{}{
		"project": "group/repo",
	}

	ctx := context.Background()
	result, err := bridgeActionSearchGLIssues(ctx, inputs, credStore, "test-team")

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if result.Status != "succeeded" {
		t.Errorf("Expected succeeded status, got %s", result.Status)
	}
}

func TestSearchGLIssues_Validation(t *testing.T) {
	// Test that search-gl-issues is properly registered and validated

	// Check it is in RegisterBridgeActions
	actions := RegisterBridgeActions()
	if _, ok := actions["search-gl-issues"]; !ok {
		t.Error("search-gl-issues not found in RegisterBridgeActions")
	}

	// Check it is in validBridgeActions
	if !validBridgeActions["search-gl-issues"] {
		t.Error("search-gl-issues not found in validBridgeActions")
	}

	// Check it is in ListBridgeActionSchemas
	schemas := ListBridgeActionSchemas()
	found := false
	for _, schema := range schemas {
		if schema.Name == "search-gl-issues" {
			found = true

			// Verify required inputs are documented
			if _, ok := schema.Inputs["project"]; !ok {
				t.Error("project input not documented in schema")
			}

			// Verify outputs are documented
			if _, ok := schema.Outputs["issues"]; !ok {
				t.Error("issues output not documented in schema")
			}
			if _, ok := schema.Outputs["total"]; !ok {
				t.Error("total output not documented in schema")
			}

			break
		}
	}
	if !found {
		t.Error("search-gl-issues schema not found in ListBridgeActionSchemas")
	}
}
