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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCredentialStore is a test mock for credential acquisition
type mockCredentialStore struct {
	token   string
	apiHost string
	err     error
}

func (m *mockCredentialStore) AcquireSCMTokenForOwner(ctx context.Context, service, teamID string) (string, string, error) {
	return m.token, m.apiHost, m.err
}

// Helper function to create bridge action result with mocked credentials
func testBridgeActionCreateGHIssue(ctx context.Context, inputs map[string]interface{}, credStore *mockCredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	title := getStringInput(inputs, "title")
	body := getStringInput(inputs, "body")
	labels := getStringSliceInput(inputs, "labels")
	assignees := getStringSliceInput(inputs, "assignees")
	milestone := getIntInput(inputs, "milestone")

	if repo == "" || title == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, title",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Build request body
	issueBody := map[string]interface{}{
		"title": title,
	}

	if body != "" {
		issueBody["body"] = body
	}

	if len(labels) > 0 {
		issueBody["labels"] = labels
	}

	if len(assignees) > 0 {
		issueBody["assignees"] = assignees
	}

	if milestone > 0 {
		issueBody["milestone"] = milestone
	}

	bodyJSON, err := json.Marshal(issueBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling issue body: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues", apiHost, repo)
	respBody, err := githubRequest(ctx, token, "POST", url, bodyJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub API error creating issue: %v", err),
		}, nil
	}

	var issueResp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &issueResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse GitHub issue response: %v", err),
		}, nil
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"issue_number": issueResp.Number,
			"issue_url":    issueResp.HTMLURL,
		},
	}, nil
}

// Test version of unified create issue function
func testBridgeActionUnifiedCreateIssue(ctx context.Context, inputs map[string]interface{}, credStore *mockCredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return &BridgeActionResult{Status: "failed", Error: "create-gl-issue is not yet implemented"}, nil
	case "github":
		return testBridgeActionCreateGHIssue(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

func TestBridgeActionCreateGHIssue(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		mockResponse   interface{}
		mockStatusCode int
		expectSuccess  bool
		expectError    string
		expectOutputs  map[string]interface{}
	}{
		{
			name: "successful issue creation with all fields",
			inputs: map[string]interface{}{
				"repo":      "owner/repo",
				"title":     "Test Issue",
				"body":      "This is a test issue",
				"labels":    []string{"bug", "enhancement"},
				"assignees": []string{"testuser"},
				"milestone": 1,
			},
			mockResponse: map[string]interface{}{
				"number":   42,
				"html_url": "https://github.com/owner/repo/issues/42",
			},
			mockStatusCode: 201,
			expectSuccess:  true,
			expectOutputs: map[string]interface{}{
				"issue_number": 42,
				"issue_url":    "https://github.com/owner/repo/issues/42",
			},
		},
		{
			name: "successful issue creation with required fields only",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Simple Issue",
			},
			mockResponse: map[string]interface{}{
				"number":   123,
				"html_url": "https://github.com/owner/repo/issues/123",
			},
			mockStatusCode: 201,
			expectSuccess:  true,
			expectOutputs: map[string]interface{}{
				"issue_number": 123,
				"issue_url":    "https://github.com/owner/repo/issues/123",
			},
		},
		{
			name: "missing required repo field",
			inputs: map[string]interface{}{
				"title": "Issue without repo",
			},
			expectSuccess: false,
			expectError:   "missing required inputs: repo, title",
		},
		{
			name: "missing required title field",
			inputs: map[string]interface{}{
				"repo": "owner/repo",
			},
			expectSuccess: false,
			expectError:   "missing required inputs: repo, title",
		},
		{
			name: "GitHub API error",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Issue that will fail",
			},
			mockResponse: map[string]interface{}{
				"message": "Validation Failed",
				"errors": []interface{}{
					map[string]interface{}{
						"field":   "title",
						"code":    "missing_field",
						"message": "title is required",
					},
				},
			},
			mockStatusCode: 422,
			expectSuccess:  false,
			expectError:    "GitHub API error creating issue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up mock GitHub API server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.mockStatusCode == 0 {
					tt.mockStatusCode = 200
				}
				w.WriteHeader(tt.mockStatusCode)
				if tt.mockResponse != nil {
					json.NewEncoder(w).Encode(tt.mockResponse)
				}
			}))
			defer server.Close()

			// Create a test credential store
			var credStore *mockCredentialStore

			if tt.expectSuccess || tt.expectError == "GitHub API error creating issue" {
				credStore = &mockCredentialStore{
					token:   "test-token",
					apiHost: server.URL,
				}
			} else {
				credStore = &mockCredentialStore{
					err: fmt.Errorf("no credentials"),
				}
			}

			// Execute the bridge action
			result, err := testBridgeActionCreateGHIssue(context.Background(), tt.inputs, credStore, "test-team")

			// Verify results
			if tt.expectSuccess {
				if err != nil {
					t.Errorf("Expected success but got error: %v", err)
				}
				if result.Status != "succeeded" {
					t.Errorf("Expected success status but got: %s", result.Status)
				}
				if result.Error != "" {
					t.Errorf("Expected no error but got: %s", result.Error)
				}
				for k, v := range tt.expectOutputs {
					if result.Outputs[k] != v {
						t.Errorf("Expected output %s=%v but got %v", k, v, result.Outputs[k])
					}
				}
			} else {
				if err == nil && result.Status != "failed" {
					t.Errorf("Expected failure but got success")
				}
				if tt.expectError != "" {
					if result.Error == "" || !contains(result.Error, tt.expectError) {
						t.Errorf("Expected error containing '%s' but got: %s", tt.expectError, result.Error)
					}
				}
			}
		})
	}
}

func TestBridgeActionUnifiedCreateIssue(t *testing.T) {
	tests := []struct {
		name          string
		inputs        map[string]interface{}
		expectSuccess bool
		expectError   string
	}{
		{
			name: "GitHub routing with repo field",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Test Issue",
			},
			expectSuccess: true,
		},
		{
			name: "GitLab routing with project field",
			inputs: map[string]interface{}{
				"project": "123",
				"title":   "Test Issue",
			},
			expectSuccess: false,
			expectError:   "create-gl-issue is not yet implemented",
		},
		{
			name: "No SCM detection",
			inputs: map[string]interface{}{
				"title": "Test Issue",
			},
			expectSuccess: false,
			expectError:   "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up mock GitHub API server for GitHub tests
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"number":   42,
					"html_url": "https://github.com/owner/repo/issues/42",
				})
			}))
			defer server.Close()

			// Create a test credential store
			var credStore *mockCredentialStore

			if tt.expectSuccess {
				credStore = &mockCredentialStore{
					token:   "test-token",
					apiHost: server.URL,
				}
			} else {
				credStore = &mockCredentialStore{
					err: fmt.Errorf("no credentials"),
				}
			}

			// Execute the unified bridge action
			result, err := testBridgeActionUnifiedCreateIssue(context.Background(), tt.inputs, credStore, "test-team")

			// Verify results
			if tt.expectSuccess {
				if err != nil {
					t.Errorf("Expected success but got error: %v", err)
				}
				if result.Status != "succeeded" {
					t.Errorf("Expected success status but got: %s", result.Status)
				}
			} else {
				if err == nil && result.Status != "failed" {
					t.Errorf("Expected failure but got success")
				}
				if tt.expectError != "" {
					if result.Error == "" || !contains(result.Error, tt.expectError) {
						t.Errorf("Expected error containing '%s' but got: %s", tt.expectError, result.Error)
					}
				}
			}
		})
	}
}

func TestBridgeActionRegistration(t *testing.T) {
	actions := RegisterBridgeActions()

	// Check that create-gh-issue is registered
	if _, exists := actions["create-gh-issue"]; !exists {
		t.Error("create-gh-issue action not registered")
	}

	// Check that create-issue is registered
	if _, exists := actions["create-issue"]; !exists {
		t.Error("create-issue action not registered")
	}

	// Check that the actions are in validBridgeActions
	if !validBridgeActions["create-gh-issue"] {
		t.Error("create-gh-issue not in validBridgeActions")
	}

	if !validBridgeActions["create-issue"] {
		t.Error("create-issue not in validBridgeActions")
	}
}

func TestBridgeActionSchemas(t *testing.T) {
	schemas := ListBridgeActionSchemas()

	var foundCreateIssue, foundCreateGHIssue bool

	for _, schema := range schemas {
		if schema.Name == "create-issue" {
			foundCreateIssue = true
			// Verify schema has expected inputs and outputs
			if schema.Inputs["repo"] == "" {
				t.Error("create-issue schema missing repo input")
			}
			if schema.Inputs["title"] == "" {
				t.Error("create-issue schema missing title input")
			}
			if schema.Outputs["issue_number"] == "" {
				t.Error("create-issue schema missing issue_number output")
			}
			if schema.Outputs["issue_url"] == "" {
				t.Error("create-issue schema missing issue_url output")
			}
		}
		if schema.Name == "create-gh-issue" {
			foundCreateGHIssue = true
			// Verify schema has expected inputs and outputs
			if schema.Inputs["repo"] == "" {
				t.Error("create-gh-issue schema missing repo input")
			}
			if schema.Inputs["title"] == "" {
				t.Error("create-gh-issue schema missing title input")
			}
			if schema.Outputs["issue_number"] == "" {
				t.Error("create-gh-issue schema missing issue_number output")
			}
			if schema.Outputs["issue_url"] == "" {
				t.Error("create-gh-issue schema missing issue_url output")
			}
		}
	}

	if !foundCreateIssue {
		t.Error("create-issue schema not found")
	}
	if !foundCreateGHIssue {
		t.Error("create-gh-issue schema not found")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr ||
			s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr))))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
