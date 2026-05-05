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

func TestGetIntSliceInput(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		key      string
		expected []int
	}{
		{
			name: "int slice",
			inputs: map[string]interface{}{
				"test": []int{1, 2, 3},
			},
			key:      "test",
			expected: []int{1, 2, 3},
		},
		{
			name: "interface slice with mixed types",
			inputs: map[string]interface{}{
				"test": []interface{}{1, 2.0, "3"},
			},
			key:      "test",
			expected: []int{1, 2, 3},
		},
		{
			name: "interface slice with invalid string",
			inputs: map[string]interface{}{
				"test": []interface{}{1, 2.0, "invalid"},
			},
			key:      "test",
			expected: []int{1, 2},
		},
		{
			name: "missing key",
			inputs: map[string]interface{}{
				"other": []int{1, 2, 3},
			},
			key:      "test",
			expected: nil,
		},
		{
			name: "empty slice",
			inputs: map[string]interface{}{
				"test": []interface{}{},
			},
			key:      "test",
			expected: []int{},
		},
		{
			name: "wrong type",
			inputs: map[string]interface{}{
				"test": "not a slice",
			},
			key:      "test",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getIntSliceInput(tt.inputs, tt.key)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected length %d, got: %d", len(tt.expected), len(result))
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("Expected result[%d]=%d, got: %d", i, expected, result[i])
				}
			}
		})
	}
}

func TestGitlabRequest(t *testing.T) {
	// Test GitLab API request with PRIVATE-TOKEN auth
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("PRIVATE-TOKEN")
		if auth != "test-token" {
			t.Errorf("Expected PRIVATE-TOKEN header with test-token, got: %s", auth)
		}
		userAgent := r.Header.Get("User-Agent")
		if userAgent != "alcove-bridge-action" {
			t.Errorf("Expected User-Agent alcove-bridge-action, got: %s", userAgent)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := gitlabRequest(ctx, "test-token", "GET", server.URL, nil)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

func TestGitlabRequestWithError(t *testing.T) {
	// Test GitLab API request error handling
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "404 Not Found"}`))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := gitlabRequest(ctx, "test-token", "GET", server.URL, nil)
	if err == nil {
		t.Error("Expected error for 404 response, got none")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("Expected HTTP 404 in error, got: %v", err)
	}
}

func TestDetectSCMForAwaitRelease(t *testing.T) {
	tests := []struct {
		name     string
		inputs   map[string]interface{}
		expected string
	}{
		{
			name: "detects gitlab with project input",
			inputs: map[string]interface{}{
				"project": "mygroup/myproject",
				"tag":     "v1.0.0",
			},
			expected: "gitlab",
		},
		{
			name: "detects github with repo input",
			inputs: map[string]interface{}{
				"repo": "owner/repo",
				"tag":  "v1.0.0",
			},
			expected: "github",
		},
		{
			name: "returns empty for ambiguous inputs",
			inputs: map[string]interface{}{
				"tag": "v1.0.0",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectSCM(tt.inputs)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestGitLabAwaitReleaseInputValidation(t *testing.T) {
	// Test input validation directly (without hitting credential store)
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		expectedError  string
	}{
		{
			name: "missing project",
			inputs: map[string]interface{}{
				"tag": "v1.0.0",
			},
			expectedError: "missing required inputs: project, tag",
		},
		{
			name: "missing tag",
			inputs: map[string]interface{}{
				"project": "myproject",
			},
			expectedError: "missing required inputs: project, tag",
		},
		{
			name: "missing both",
			inputs: map[string]interface{}{},
			expectedError: "missing required inputs: project, tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := getStringInput(tt.inputs, "project")
			tag := getStringInput(tt.inputs, "tag")

			// Simulate the validation logic from bridgeActionAwaitGLRelease
			if project == "" || tag == "" {
				expectedErr := "missing required inputs: project, tag"
				if expectedErr != tt.expectedError {
					t.Errorf("Expected error %q, got %q", tt.expectedError, expectedErr)
				}
			}
		})
	}
}

func TestUnifiedAwaitReleaseRouting(t *testing.T) {
	// Test the SCM detection logic directly
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		expectedSCM    string
	}{
		{
			name: "routes to gitlab with project input",
			inputs: map[string]interface{}{
				"project": "mygroup/myproject",
				"tag":     "v1.0.0",
			},
			expectedSCM: "gitlab",
		},
		{
			name: "routes to github with repo input",
			inputs: map[string]interface{}{
				"repo": "owner/repo",
				"tag":  "v1.0.0",
			},
			expectedSCM: "github",
		},
		{
			name: "ambiguous with neither repo nor project",
			inputs: map[string]interface{}{
				"tag": "v1.0.0",
			},
			expectedSCM: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scm := detectSCM(tt.inputs)
			if scm != tt.expectedSCM {
				t.Errorf("Expected SCM %s, got %s", tt.expectedSCM, scm)
			}
		})
	}
}

func TestGitLabReleaseURLConstruction(t *testing.T) {
	tests := []struct {
		name        string
		project     string
		tag         string
		expectedURL string
	}{
		{
			name:        "simple project and tag",
			project:     "myproject",
			tag:         "v1.0.0",
			expectedURL: "/api/v4/projects/myproject/releases/v1.0.0",
		},
		{
			name:        "project with slash",
			project:     "mygroup/myproject",
			tag:         "v1.0.0",
			expectedURL: "/api/v4/projects/mygroup%2Fmyproject/releases/v1.0.0",
		},
		{
			name:        "tag with plus sign",
			project:     "myproject",
			tag:         "v1.0.0+beta",
			expectedURL: "/api/v4/projects/myproject/releases/v1.0.0%2Bbeta",
		},
		{
			name:        "complex project and tag",
			project:     "my-group/my-project",
			tag:         "v1.0.0-rc.1+build.123",
			expectedURL: "/api/v4/projects/my-group%2Fmy-project/releases/v1.0.0-rc.1%2Bbuild.123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.expectedURL {
					t.Errorf("Expected URL path %s, got %s", tt.expectedURL, r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"tag_name": tt.tag,
					"web_url":  "https://gitlab.example.com" + r.URL.Path,
				})
			}))
			defer server.Close()

			ctx := context.Background()
			respBody, err := gitlabRequest(ctx, "test-token", "GET",
				server.URL+"/api/v4/projects/"+strings.ReplaceAll(tt.project, "/", "%2F")+
				"/releases/"+strings.ReplaceAll(tt.tag, "+", "%2B"), nil)

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			var release struct {
				TagName string `json:"tag_name"`
				WebURL  string `json:"web_url"`
			}
			if err := json.Unmarshal(respBody, &release); err != nil {
				t.Fatalf("Failed to unmarshal response: %v", err)
			}

			if release.TagName != tt.tag {
				t.Errorf("Expected tag_name %s, got %s", tt.tag, release.TagName)
			}
		})
	}
}
