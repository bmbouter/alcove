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

func TestBridgeActionSearchGHIssues(t *testing.T) {
	// Test successful search with results
	t.Run("successful search with results", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify request
			if r.Method != "GET" {
				t.Errorf("Expected GET request, got %s", r.Method)
			}
			if !strings.Contains(r.URL.RawQuery, "repo:owner/repo") {
				t.Errorf("Expected repo qualifier in query, got: %s", r.URL.RawQuery)
			}
			if !strings.Contains(r.URL.RawQuery, "is:open") {
				t.Errorf("Expected user query in URL, got: %s", r.URL.RawQuery)
			}
			if !strings.Contains(r.URL.RawQuery, "per_page=30") {
				t.Errorf("Expected per_page=30 in query, got: %s", r.URL.RawQuery)
			}

			// Mock response
			resp := map[string]interface{}{
				"total_count":        2,
				"incomplete_results": false,
				"items": []map[string]interface{}{
					{
						"number":   123,
						"title":    "Test issue 1",
						"state":    "open",
						"html_url": "https://github.com/owner/repo/issues/123",
						"labels": []map[string]interface{}{
							{"name": "bug"},
							{"name": "priority:high"},
						},
					},
					{
						"number":   124,
						"title":    "Test issue 2",
						"state":    "closed",
						"html_url": "https://github.com/owner/repo/issues/124",
						"labels":   []map[string]interface{}{},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		inputs := map[string]interface{}{
			"repo":        "owner/repo",
			"query":       "is:open",
			"max_results": 30,
		}

		result, err := bridgeActionSearchGHIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "succeeded" {
			t.Errorf("Expected status 'succeeded', got: %s", result.Status)
		}

		issues, ok := result.Outputs["issues"].([]map[string]interface{})
		if !ok {
			t.Fatalf("Expected issues to be []map[string]interface{}, got: %T", result.Outputs["issues"])
		}

		if len(issues) != 2 {
			t.Errorf("Expected 2 issues, got: %d", len(issues))
		}

		// Check first issue
		issue1 := issues[0]
		if issue1["number"] != 123 {
			t.Errorf("Expected issue number 123, got: %v", issue1["number"])
		}
		if issue1["title"] != "Test issue 1" {
			t.Errorf("Expected title 'Test issue 1', got: %v", issue1["title"])
		}
		if issue1["state"] != "open" {
			t.Errorf("Expected state 'open', got: %v", issue1["state"])
		}
		if issue1["url"] != "https://github.com/owner/repo/issues/123" {
			t.Errorf("Expected URL 'https://github.com/owner/repo/issues/123', got: %v", issue1["url"])
		}

		labels, ok := issue1["labels"].([]string)
		if !ok {
			t.Fatalf("Expected labels to be []string, got: %T", issue1["labels"])
		}
		if len(labels) != 2 || labels[0] != "bug" || labels[1] != "priority:high" {
			t.Errorf("Expected labels ['bug', 'priority:high'], got: %v", labels)
		}

		// Check second issue (empty labels)
		issue2 := issues[1]
		labels2, ok := issue2["labels"].([]string)
		if !ok {
			t.Fatalf("Expected labels to be []string, got: %T", issue2["labels"])
		}
		if len(labels2) != 0 {
			t.Errorf("Expected empty labels, got: %v", labels2)
		}

		// Check total count
		if result.Outputs["total"] != 2 {
			t.Errorf("Expected total count 2, got: %v", result.Outputs["total"])
		}
	})

	// Test search with zero results
	t.Run("search with zero results", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"total_count":        0,
				"incomplete_results": false,
				"items":              []map[string]interface{}{},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		inputs := map[string]interface{}{
			"repo":  "owner/repo",
			"query": "no-results",
		}

		result, err := bridgeActionSearchGHIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "succeeded" {
			t.Errorf("Expected status 'succeeded' for zero results, got: %s", result.Status)
		}

		issues := result.Outputs["issues"].([]map[string]interface{})
		if len(issues) != 0 {
			t.Errorf("Expected zero issues, got: %d", len(issues))
		}

		if result.Outputs["total"] != 0 {
			t.Errorf("Expected total count 0, got: %v", result.Outputs["total"])
		}
	})

	// Test missing required inputs
	t.Run("missing required inputs", func(t *testing.T) {
		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: "https://api.github.com",
		}

		// Missing repo
		inputs1 := map[string]interface{}{
			"query": "is:open",
		}
		result1, err := bridgeActionSearchGHIssues(context.Background(), inputs1, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if result1.Status != "failed" {
			t.Errorf("Expected status 'failed' for missing repo, got: %s", result1.Status)
		}
		if !strings.Contains(result1.Error, "missing required inputs") {
			t.Errorf("Expected missing inputs error, got: %s", result1.Error)
		}

		// Missing query
		inputs2 := map[string]interface{}{
			"repo": "owner/repo",
		}
		result2, err := bridgeActionSearchGHIssues(context.Background(), inputs2, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}
		if result2.Status != "failed" {
			t.Errorf("Expected status 'failed' for missing query, got: %s", result2.Status)
		}
		if !strings.Contains(result2.Error, "missing required inputs") {
			t.Errorf("Expected missing inputs error, got: %s", result2.Error)
		}
	})

	// Test max_results clamping
	t.Run("max_results clamping", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.RawQuery
			if strings.Contains(query, "per_page=20") {
				// Default value used
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"total_count": 0,
					"items":       []map[string]interface{}{},
				})
				return
			}
			if strings.Contains(query, "per_page=100") {
				// Clamped to max value
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"total_count": 0,
					"items":       []map[string]interface{}{},
				})
				return
			}
			t.Errorf("Unexpected per_page value in query: %s", query)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		// Test default (0 should become 20)
		inputs1 := map[string]interface{}{
			"repo":  "owner/repo",
			"query": "is:open",
		}
		result1, err := bridgeActionSearchGHIssues(context.Background(), inputs1, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error for default, got: %v", err)
		}
		if result1.Status != "succeeded" {
			t.Errorf("Expected success for default max_results, got: %s", result1.Status)
		}

		// Test clamping (150 should become 100)
		inputs2 := map[string]interface{}{
			"repo":        "owner/repo",
			"query":       "is:open",
			"max_results": 150,
		}
		result2, err := bridgeActionSearchGHIssues(context.Background(), inputs2, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error for clamped, got: %v", err)
		}
		if result2.Status != "succeeded" {
			t.Errorf("Expected success for clamped max_results, got: %s", result2.Status)
		}
	})

	// Test incomplete results
	t.Run("incomplete results", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"total_count":        100,
				"incomplete_results": true,
				"items":              []map[string]interface{}{},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		inputs := map[string]interface{}{
			"repo":  "owner/repo",
			"query": "is:open",
		}

		result, err := bridgeActionSearchGHIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "succeeded" {
			t.Errorf("Expected status 'succeeded', got: %s", result.Status)
		}

		// Check that incomplete_results is included in outputs
		incompleteResults, ok := result.Outputs["incomplete_results"]
		if !ok {
			t.Error("Expected incomplete_results in outputs")
		}
		if incompleteResults != true {
			t.Errorf("Expected incomplete_results to be true, got: %v", incompleteResults)
		}
	})

	// Test GitHub API error
	t.Run("github api error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message": "API rate limit exceeded"}`))
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		inputs := map[string]interface{}{
			"repo":  "owner/repo",
			"query": "is:open",
		}

		result, err := bridgeActionSearchGHIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "failed" {
			t.Errorf("Expected status 'failed' for API error, got: %s", result.Status)
		}

		if !strings.Contains(result.Error, "GitHub Search API error") {
			t.Errorf("Expected GitHub Search API error message, got: %s", result.Error)
		}
	})
}

func TestBridgeActionSearchIssues(t *testing.T) {
	// Test GitHub routing
	t.Run("routes to GitHub", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resp := map[string]interface{}{
				"total_count": 0,
				"items":       []map[string]interface{}{},
			}
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		credStore := &mockCredentialStore{
			token:   "test-token",
			apiHost: server.URL,
		}

		inputs := map[string]interface{}{
			"repo":  "owner/repo",
			"query": "is:open",
		}

		result, err := bridgeActionSearchIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "succeeded" {
			t.Errorf("Expected status 'succeeded', got: %s", result.Status)
		}

		// Should have GitHub-style outputs
		if _, ok := result.Outputs["issues"]; !ok {
			t.Error("Expected 'issues' in outputs for GitHub routing")
		}
		if _, ok := result.Outputs["total"]; !ok {
			t.Error("Expected 'total' in outputs for GitHub routing")
		}
	})

	// Test JIRA routing (mock the JIRA search function)
	t.Run("routes to JIRA", func(t *testing.T) {
		// Since we can't easily mock the JIRA function without a full server,
		// we'll test that the routing logic works correctly
		inputs := map[string]interface{}{
			"jql": "project = TEST",
		}

		// This will fail because we don't have a real JIRA server,
		// but it should attempt to call the JIRA function
		credStore := &mockCredentialStore{
			err: fmt.Errorf("no JIRA token configured"),
		}

		result, err := bridgeActionSearchIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "failed" {
			t.Errorf("Expected status 'failed' (no JIRA token), got: %s", result.Status)
		}

		if !strings.Contains(result.Error, "JIRA token") {
			t.Errorf("Expected JIRA token error, got: %s", result.Error)
		}
	})

	// Test unknown inputs
	t.Run("unknown search target", func(t *testing.T) {
		credStore := &mockCredentialStore{
			token: "test-token",
		}

		inputs := map[string]interface{}{
			"unknown": "value",
		}

		result, err := bridgeActionSearchIssues(context.Background(), inputs, credStore, "test-team")
		if err != nil {
			t.Fatalf("Expected no error, got: %v", err)
		}

		if result.Status != "failed" {
			t.Errorf("Expected status 'failed' for unknown inputs, got: %s", result.Status)
		}

		if !strings.Contains(result.Error, "cannot detect search target") {
			t.Errorf("Expected detection error, got: %s", result.Error)
		}
	})
}

func TestValidBridgeActions(t *testing.T) {
	// Test that search actions are in validBridgeActions
	if !validBridgeActions["search-gh-issues"] {
		t.Error("Expected search-gh-issues to be in validBridgeActions")
	}
	if !validBridgeActions["search-issues"] {
		t.Error("Expected search-issues to be in validBridgeActions")
	}
}

func TestBridgeActionRegistration(t *testing.T) {
	// Test that search actions are registered
	handlers := RegisterBridgeActions()

	if _, ok := handlers["search-gh-issues"]; !ok {
		t.Error("Expected search-gh-issues to be registered")
	}
	if _, ok := handlers["search-issues"]; !ok {
		t.Error("Expected search-issues to be registered")
	}
}

func TestBridgeActionSchemas(t *testing.T) {
	// Test that search action schemas are present
	schemas := ListBridgeActionSchemas()

	var foundGHSearch, foundUnified bool
	for _, schema := range schemas {
		switch schema.Name {
		case "search-gh-issues":
			foundGHSearch = true
			// Validate schema structure
			if schema.Description == "" {
				t.Error("search-gh-issues schema missing description")
			}
			if _, ok := schema.Inputs["repo"]; !ok {
				t.Error("search-gh-issues schema missing 'repo' input")
			}
			if _, ok := schema.Inputs["query"]; !ok {
				t.Error("search-gh-issues schema missing 'query' input")
			}
			if _, ok := schema.Outputs["issues"]; !ok {
				t.Error("search-gh-issues schema missing 'issues' output")
			}
			if _, ok := schema.Outputs["total"]; !ok {
				t.Error("search-gh-issues schema missing 'total' output")
			}
		case "search-issues":
			foundUnified = true
			// Validate unified schema
			if schema.Description == "" {
				t.Error("search-issues schema missing description")
			}
			if _, ok := schema.Inputs["repo"]; !ok {
				t.Error("search-issues schema missing 'repo' input")
			}
			if _, ok := schema.Inputs["jql"]; !ok {
				t.Error("search-issues schema missing 'jql' input")
			}
		}
	}

	if !foundGHSearch {
		t.Error("search-gh-issues schema not found")
	}
	if !foundUnified {
		t.Error("search-issues schema not found")
	}
}

func TestBridgeActionCreateGHIssue(t *testing.T) {
	tests := []struct {
		name            string
		inputs          map[string]interface{}
		expectedStatus  string
		expectedError   string
		expectedOutputs map[string]interface{}
		mockResponse    string
		mockStatusCode  int
	}{
		{
			name: "happy path with all fields",
			inputs: map[string]interface{}{
				"repo":       "owner/repo",
				"title":      "Test Issue",
				"body":       "Issue description",
				"labels":     []string{"bug", "high-priority"},
				"assignees":  []string{"john", "jane"},
				"milestone":  5,
			},
			expectedStatus: "succeeded",
			expectedOutputs: map[string]interface{}{
				"issue_number": 42,
				"issue_url":    "https://github.com/owner/repo/issues/42",
			},
			mockResponse:   `{"number": 42, "html_url": "https://github.com/owner/repo/issues/42"}`,
			mockStatusCode: 201,
		},
		{
			name: "required fields only",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Minimal Issue",
			},
			expectedStatus: "succeeded",
			expectedOutputs: map[string]interface{}{
				"issue_number": 43,
				"issue_url":    "https://github.com/owner/repo/issues/43",
			},
			mockResponse:   `{"number": 43, "html_url": "https://github.com/owner/repo/issues/43"}`,
			mockStatusCode: 201,
		},
		{
			name: "missing repo",
			inputs: map[string]interface{}{
				"title": "Test Issue",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: repo, title",
		},
		{
			name: "missing title",
			inputs: map[string]interface{}{
				"repo": "owner/repo",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: repo, title",
		},
		{
			name: "GitHub API error",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Test Issue",
			},
			expectedStatus:  "failed",
			expectedError:   "GitHub API error creating issue: HTTP 422:",
			mockResponse:    `{"message": "Validation Failed"}`,
			mockStatusCode:  422,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *httptest.Server
			if tt.expectedStatus != "failed" || strings.Contains(tt.expectedError, "GitHub API error") {
				// Set up mock server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// Verify request method and path
					if r.Method != "POST" || !strings.Contains(r.URL.Path, "/issues") {
						t.Errorf("Expected POST request to /issues endpoint, got %s %s", r.Method, r.URL.Path)
					}

					// Verify Authorization header
					auth := r.Header.Get("Authorization")
					if !strings.HasPrefix(auth, "token ") {
						t.Errorf("Expected token auth, got: %s", auth)
					}

					// Verify request body for the full fields test
					if tt.name == "happy path with all fields" {
						var body map[string]interface{}
						if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
							if body["title"] != "Test Issue" {
								t.Errorf("Expected title 'Test Issue', got %v", body["title"])
							}
							if body["body"] != "Issue description" {
								t.Errorf("Expected body 'Issue description', got %v", body["body"])
							}
							if labels, ok := body["labels"].([]interface{}); !ok || len(labels) != 2 {
								t.Errorf("Expected 2 labels, got %v", body["labels"])
							}
							if assignees, ok := body["assignees"].([]interface{}); !ok || len(assignees) != 2 {
								t.Errorf("Expected 2 assignees, got %v", body["assignees"])
							}
							if milestone, ok := body["milestone"].(float64); !ok || milestone != 5 {
								t.Errorf("Expected milestone 5, got %v", body["milestone"])
							}
						}
					}

					// Verify required-only test does not include optional fields
					if tt.name == "required fields only" {
						var body map[string]interface{}
						if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
							if _, exists := body["body"]; exists {
								t.Error("Expected no body field for required-only test")
							}
							if _, exists := body["labels"]; exists {
								t.Error("Expected no labels field for required-only test")
							}
							if _, exists := body["assignees"]; exists {
								t.Error("Expected no assignees field for required-only test")
							}
							if _, exists := body["milestone"]; exists {
								t.Error("Expected no milestone field for required-only test")
							}
						}
					}

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tt.mockStatusCode)
					w.Write([]byte(tt.mockResponse))
				}))
				defer server.Close()
			}

			// Create mock credential store
			var apiHost string
			if server != nil {
				apiHost = server.URL
			}
			credStore := &mockCredentialStore{
				token:   "test-token",
				apiHost: apiHost,
			}

			// Execute the bridge action
			ctx := context.Background()
			result, err := bridgeActionCreateGHIssue(ctx, tt.inputs, credStore, "test-team")

			// Verify the result
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			if tt.expectedError != "" {
				if !strings.Contains(result.Error, tt.expectedError) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.expectedError, result.Error)
				}
			}

			if tt.expectedOutputs != nil {
				for key, expectedValue := range tt.expectedOutputs {
					if result.Outputs[key] != expectedValue {
						t.Errorf("Expected output %s = %v, got %v", key, expectedValue, result.Outputs[key])
					}
				}
			}
		})
	}
}

func TestBridgeActionUnifiedCreateIssue(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		expectedStatus string
		expectedError  string
	}{
		{
			name: "GitHub detection with repo input",
			inputs: map[string]interface{}{
				"repo":  "owner/repo",
				"title": "Test Issue",
			},
			expectedStatus: "succeeded", // Assuming GitHub handler succeeds
		},
		{
			name: "GitLab detection with project input",
			inputs: map[string]interface{}{
				"project": "group/project",
				"title":   "Test Issue",
			},
			expectedStatus: "failed",
			expectedError:  "create-gl-issue is not yet implemented (see #563)",
		},
		{
			name: "No SCM detection",
			inputs: map[string]interface{}{
				"title": "Test Issue",
			},
			expectedStatus: "failed",
			expectedError:  "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *httptest.Server
			if tt.name == "GitHub detection with repo input" {
				// Set up mock server for GitHub test
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(201)
					w.Write([]byte(`{"number": 44, "html_url": "https://github.com/owner/repo/issues/44"}`))
				}))
				defer server.Close()
			}

			// Create mock credential store
			var apiHost string
			if server != nil {
				apiHost = server.URL
			}
			credStore := &mockCredentialStore{
				token:   "test-token",
				apiHost: apiHost,
			}

			// Execute the unified bridge action
			ctx := context.Background()
			result, err := bridgeActionUnifiedCreateIssue(ctx, tt.inputs, credStore, "test-team")

			// Verify the result
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			if tt.expectedError != "" {
				if !strings.Contains(result.Error, tt.expectedError) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.expectedError, result.Error)
				}
			}
		})
	}
}
