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

func TestBridgeActionCreateMRs(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		serverHandler  func(w http.ResponseWriter, r *http.Request)
		expectedStatus string
		expectedError  string
		expectedOutput map[string]interface{}
	}{
		{
			name: "happy path - all projects succeed",
			inputs: map[string]interface{}{
				"projects":      []string{"project1", "project2"},
				"source_branch": "feature-branch",
				"target_branch": "main",
				"title":         "Test MR",
				"description":   "Test description",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					// Branch exists check
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name": "feature-branch"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
					// Create MR
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"iid": 123, "web_url": "https://gitlab.com/test/mr/123"}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
			},
			expectedStatus: "succeeded",
			expectedOutput: map[string]interface{}{
				"mr_iids":         []int{123, 123},
				"mr_urls":         []string{"https://gitlab.com/test/mr/123", "https://gitlab.com/test/mr/123"},
				"projects":        []string{"project1", "project2"},
				"failed_projects": []string{},
			},
		},
		{
			name: "missing source_branch",
			inputs: map[string]interface{}{
				"projects": []string{"project1"},
				"title":    "Test MR",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: source_branch, title",
		},
		{
			name: "missing title",
			inputs: map[string]interface{}{
				"projects":      []string{"project1"},
				"source_branch": "feature-branch",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: source_branch, title",
		},
		{
			name: "empty projects array",
			inputs: map[string]interface{}{
				"projects":      []string{},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			expectedStatus: "failed",
			expectedError:  "missing required input: projects (array of project paths)",
		},
		{
			name: "branch not found - silently skipped",
			inputs: map[string]interface{}{
				"projects":      []string{"project1", "project2"},
				"source_branch": "missing-branch",
				"title":         "Test MR",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					// Branch doesn't exist
					w.WriteHeader(http.StatusNotFound)
					w.Write([]byte(`{"message": "404 Branch Not Found"}`))
					return
				}
			},
			expectedStatus: "succeeded",
			expectedOutput: map[string]interface{}{
				"mr_iids":         []int{},
				"mr_urls":         []string{},
				"projects":        []string{},
				"failed_projects": []string{},
			},
		},
		{
			name: "MR already exists - reuse existing",
			inputs: map[string]interface{}{
				"projects":      []string{"project1"},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					// Branch exists
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name": "feature-branch"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
					// MR already exists
					w.WriteHeader(http.StatusConflict)
					w.Write([]byte(`{"message": "Another open merge request already exists"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "GET" {
					// Find existing MR
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`[{"iid": 456, "web_url": "https://gitlab.com/test/mr/456"}]`))
					return
				}
			},
			expectedStatus: "succeeded",
			expectedOutput: map[string]interface{}{
				"mr_iids":         []int{456},
				"mr_urls":         []string{"https://gitlab.com/test/mr/456"},
				"projects":        []string{"project1"},
				"failed_projects": []string{},
			},
		},
		{
			name: "draft flag sets draft to true",
			inputs: map[string]interface{}{
				"projects":      []string{"project1"},
				"source_branch": "feature-branch",
				"title":         "Test MR",
				"draft":         true,
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name": "feature-branch"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
					// Check that draft was set
					var body map[string]interface{}
					json.NewDecoder(r.Body).Decode(&body)
					if draft, ok := body["draft"].(bool); !ok || !draft {
						t.Errorf("Expected draft=true in request body, got %v", body["draft"])
					}
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"iid": 789, "web_url": "https://gitlab.com/test/mr/789"}`))
					return
				}
			},
			expectedStatus: "succeeded",
		},
		{
			name: "default target_branch to main",
			inputs: map[string]interface{}{
				"projects":      []string{"project1"},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name": "feature-branch"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
					// Check that target_branch defaults to main
					var body map[string]interface{}
					json.NewDecoder(r.Body).Decode(&body)
					if target, ok := body["target_branch"].(string); !ok || target != "main" {
						t.Errorf("Expected target_branch=main, got %v", body["target_branch"])
					}
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`{"iid": 101, "web_url": "https://gitlab.com/test/mr/101"}`))
					return
				}
			},
			expectedStatus: "succeeded",
		},
		{
			name: "some projects fail API call",
			inputs: map[string]interface{}{
				"projects":      []string{"project1", "project2"},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"name": "feature-branch"}`))
					return
				}
				if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
					if strings.Contains(r.URL.Path, "project1") {
						// First project succeeds
						w.WriteHeader(http.StatusCreated)
						w.Write([]byte(`{"iid": 111, "web_url": "https://gitlab.com/test/mr/111"}`))
					} else {
						// Second project fails
						w.WriteHeader(http.StatusInternalServerError)
						w.Write([]byte(`{"message": "Internal server error"}`))
					}
					return
				}
			},
			expectedStatus: "succeeded",
			expectedOutput: map[string]interface{}{
				"mr_iids":         []int{111},
				"mr_urls":         []string{"https://gitlab.com/test/mr/111"},
				"projects":        []string{"project1"},
				"failed_projects": []string{"project2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var server *httptest.Server
			if tt.serverHandler != nil {
				server = httptest.NewServer(http.HandlerFunc(tt.serverHandler))
				defer server.Close()
			}

			credStore := &mockCredentialStore{
				token:   "test-token",
				apiHost: server.URL,
			}

			result, err := bridgeActionCreateMRs(context.Background(), tt.inputs, credStore, "test-team")
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result.Status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, result.Status)
			}

			if tt.expectedError != "" {
				if !strings.Contains(result.Error, tt.expectedError) {
					t.Errorf("Expected error to contain '%s', got '%s'", tt.expectedError, result.Error)
				}
			}

			if tt.expectedOutput != nil {
				for key, expected := range tt.expectedOutput {
					actual, exists := result.Outputs[key]
					if !exists {
						t.Errorf("Expected output key '%s' not found", key)
						continue
					}

					// Convert slices to strings for comparison
					expectedStr := fmt.Sprintf("%v", expected)
					actualStr := fmt.Sprintf("%v", actual)
					if expectedStr != actualStr {
						t.Errorf("Expected output %s=%s, got %s", key, expectedStr, actualStr)
					}
				}
			}
		})
	}
}

func TestBridgeActionRegistration(t *testing.T) {
	actions := RegisterBridgeActions()

	// Test that create-mrs is registered
	if _, exists := actions["create-mrs"]; !exists {
		t.Error("create-mrs action not registered")
	}

	// Test that create-prs is registered (fixing existing gap)
	if _, exists := actions["create-prs"]; !exists {
		t.Error("create-prs action not registered")
	}
}

func TestCreateMRsValidation(t *testing.T) {
	// Test that create-mrs is in validBridgeActions
	if !validBridgeActions["create-mrs"] {
		t.Error("create-mrs not in validBridgeActions")
	}

	// Test that create-prs is in validBridgeActions (fixing existing gap)
	if !validBridgeActions["create-prs"] {
		t.Error("create-prs not in validBridgeActions")
	}
}

func TestCreateMRsSingleProjectDraftSupport(t *testing.T) {
	// Test that the single create-mr action now supports draft
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/merge_requests") && r.Method == "POST" {
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if draft, ok := body["draft"].(bool); !ok || !draft {
				t.Errorf("Expected draft=true in single MR request body, got %v", body["draft"])
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"iid": 999, "web_url": "https://gitlab.com/test/mr/999"}`))
			return
		}
	}))
	defer server.Close()

	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
	}

	inputs := map[string]interface{}{
		"project":       "test-project",
		"source_branch": "feature-branch",
		"target_branch": "main",
		"title":         "Test Draft MR",
		"draft":         true,
	}

	result, err := bridgeActionCreateMR(context.Background(), inputs, credStore, "test-team")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("Expected success, got status: %s, error: %s", result.Status, result.Error)
	}
}
