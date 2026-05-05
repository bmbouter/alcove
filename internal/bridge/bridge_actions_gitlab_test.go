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
	"time"
)

func TestBridgeActionAwaitGLRelease(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		mockResponse   string
		mockStatusCode int
		retries        int // Number of 404 responses before success
		expectStatus   string
		expectError    string
		expectURL      string
	}{
		{
			name: "release found immediately",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"tag":     "v1.0.0",
				"timeout": 60,
			},
			mockResponse: `{
				"tag_name": "v1.0.0",
				"web_url": "https://gitlab.example.com/group/repo/-/releases/v1.0.0"
			}`,
			mockStatusCode: 200,
			expectStatus:   "succeeded",
			expectURL:      "https://gitlab.example.com/group/repo/-/releases/v1.0.0",
		},
		{
			name: "release found after polling",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"tag":     "v1.0.0",
				"timeout": 90,
			},
			mockResponse: `{
				"tag_name": "v1.0.0",
				"web_url": "https://gitlab.example.com/group/repo/-/releases/v1.0.0"
			}`,
			mockStatusCode: 200,
			retries:        2, // Two 404s then success
			expectStatus:   "succeeded",
			expectURL:      "https://gitlab.example.com/group/repo/-/releases/v1.0.0",
		},
		{
			name: "missing project input",
			inputs: map[string]interface{}{
				"tag":     "v1.0.0",
				"timeout": 60,
			},
			expectStatus: "failed",
			expectError:  "missing required inputs: project, tag",
		},
		{
			name: "missing tag input",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"timeout": 60,
			},
			expectStatus: "failed",
			expectError:  "missing required inputs: project, tag",
		},
		{
			name: "default timeout",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"tag":     "v1.0.0",
			},
			mockResponse: `{
				"tag_name": "v1.0.0",
				"web_url": "https://gitlab.example.com/group/repo/-/releases/v1.0.0"
			}`,
			mockStatusCode: 200,
			expectStatus:   "succeeded",
			expectURL:      "https://gitlab.example.com/group/repo/-/releases/v1.0.0",
		},
		{
			name: "tag with special characters",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"tag":     "v1.0.0+build.123",
				"timeout": 60,
			},
			mockResponse: `{
				"tag_name": "v1.0.0+build.123",
				"_links": {
					"self": "https://gitlab.example.com/api/v4/projects/123/releases/v1.0.0+build.123"
				}
			}`,
			mockStatusCode: 200,
			expectStatus:   "succeeded",
			expectURL:      "https://gitlab.example.com/api/v4/projects/123/releases/v1.0.0+build.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++

				// Check for project and tag URL encoding
				if tt.inputs["project"] != nil && tt.inputs["tag"] != nil {
					expectedPath := "/api/v4/projects/group%2Frepo/releases/" + strings.ReplaceAll(tt.inputs["tag"].(string), "+", "%2B")
					if r.URL.Path != expectedPath {
						t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
					}
				}

				// Check authorization header
				if auth := r.Header.Get("PRIVATE-TOKEN"); auth != "mock-token" {
					t.Errorf("Expected PRIVATE-TOKEN header, got: %s", auth)
				}

				// Simulate retries if needed
				if tt.retries > 0 && requestCount <= tt.retries {
					w.WriteHeader(404)
					w.Write([]byte(`{"message": "404 Not Found"}`))
					return
				}

				w.WriteHeader(tt.mockStatusCode)
				if tt.mockResponse != "" {
					w.Write([]byte(tt.mockResponse))
				}
			}))
			defer server.Close()

			// Create a mock credential store
			credStore := &CredentialStore{} // This would need to be mocked properly

			// Mock the credential store method
			originalAcquire := credStore.AcquireSCMTokenForOwner
			credStore.AcquireSCMTokenForOwner = func(ctx context.Context, scm, teamID string) (string, string, error) {
				if scm != "gitlab" {
					t.Errorf("Expected SCM 'gitlab', got '%s'", scm)
				}
				return "mock-token", server.URL, nil
			}
			defer func() { credStore.AcquireSCMTokenForOwner = originalAcquire }()

			ctx := context.Background()
			result, err := bridgeActionAwaitGLRelease(ctx, tt.inputs, credStore, "test-team")

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if result.Status != tt.expectStatus {
				t.Errorf("Expected status %s, got %s", tt.expectStatus, result.Status)
			}

			if tt.expectError != "" {
				if result.Error != tt.expectError {
					t.Errorf("Expected error %s, got %s", tt.expectError, result.Error)
				}
			}

			if tt.expectURL != "" {
				if url, ok := result.Outputs["release_url"].(string); ok {
					if url != tt.expectURL {
						t.Errorf("Expected URL %s, got %s", tt.expectURL, url)
					}
				} else {
					t.Error("Expected release_url in outputs")
				}
			}
		})
	}
}

func TestBridgeActionAwaitGLReleaseTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"message": "404 Not Found"}`))
	}))
	defer server.Close()

	credStore := &CredentialStore{}
	credStore.AcquireSCMTokenForOwner = func(ctx context.Context, scm, teamID string) (string, string, error) {
		return "mock-token", server.URL, nil
	}

	inputs := map[string]interface{}{
		"project": "group/repo",
		"tag":     "v1.0.0",
		"timeout": 1, // Very short timeout
	}

	ctx := context.Background()
	result, err := bridgeActionAwaitGLRelease(ctx, inputs, credStore, "test-team")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected status 'failed', got %s", result.Status)
	}

	if !strings.Contains(result.Error, "timed out") {
		t.Errorf("Expected timeout error, got: %s", result.Error)
	}
}

func TestBridgeActionAwaitGLReleaseCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response to ensure context cancellation is tested
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(404)
		w.Write([]byte(`{"message": "404 Not Found"}`))
	}))
	defer server.Close()

	credStore := &CredentialStore{}
	credStore.AcquireSCMTokenForOwner = func(ctx context.Context, scm, teamID string) (string, string, error) {
		return "mock-token", server.URL, nil
	}

	inputs := map[string]interface{}{
		"project": "group/repo",
		"tag":     "v1.0.0",
		"timeout": 60,
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel the context immediately
	cancel()

	result, err := bridgeActionAwaitGLRelease(ctx, inputs, credStore, "test-team")

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected status 'failed', got %s", result.Status)
	}

	if result.Error != "context cancelled" {
		t.Errorf("Expected 'context cancelled' error, got: %s", result.Error)
	}
}

func TestBridgeActionUnifiedAwaitRelease(t *testing.T) {
	tests := []struct {
		name         string
		inputs       map[string]interface{}
		expectSCM    string
		expectError  string
	}{
		{
			name: "detects GitLab from project input",
			inputs: map[string]interface{}{
				"project": "group/repo",
				"tag":     "v1.0.0",
			},
			expectSCM: "gitlab",
		},
		{
			name: "detects GitHub from repo input",
			inputs: map[string]interface{}{
				"repo": "owner/repo",
				"tag":  "v1.0.0",
			},
			expectSCM: "github",
		},
		{
			name: "fails on ambiguous inputs",
			inputs: map[string]interface{}{
				"tag": "v1.0.0",
			},
			expectError: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock server for successful responses
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.expectSCM == "gitlab" {
					w.Write([]byte(`{
						"tag_name": "v1.0.0",
						"web_url": "https://gitlab.example.com/group/repo/-/releases/v1.0.0"
					}`))
				} else if tt.expectSCM == "github" {
					w.Write([]byte(`{
						"tag_name": "v1.0.0",
						"html_url": "https://github.com/owner/repo/releases/tag/v1.0.0"
					}`))
				}
			}))
			defer server.Close()

			credStore := &CredentialStore{}
			credStore.AcquireSCMTokenForOwner = func(ctx context.Context, scm, teamID string) (string, string, error) {
				if tt.expectSCM != "" && scm != tt.expectSCM {
					t.Errorf("Expected SCM %s, got %s", tt.expectSCM, scm)
				}
				return "mock-token", server.URL, nil
			}

			ctx := context.Background()
			result, err := bridgeActionUnifiedAwaitRelease(ctx, tt.inputs, credStore, "test-team")

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tt.expectError != "" {
				if result.Status != "failed" {
					t.Errorf("Expected status 'failed', got %s", result.Status)
				}
				if result.Error != tt.expectError {
					t.Errorf("Expected error %s, got %s", tt.expectError, result.Error)
				}
			} else {
				if result.Status != "succeeded" {
					t.Errorf("Expected status 'succeeded', got %s", result.Status)
				}
			}
		})
	}
}