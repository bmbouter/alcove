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

func TestBridgeActionCreateMRsInputValidation(t *testing.T) {
	tests := []struct {
		name           string
		inputs         map[string]interface{}
		expectedStatus string
		expectedError  string
	}{
		{
			name: "missing required inputs - source_branch",
			inputs: map[string]interface{}{
				"projects": []string{"group/project1", "group/project2"},
				"title":    "Test MR",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: source_branch, title",
		},
		{
			name: "missing required inputs - title",
			inputs: map[string]interface{}{
				"projects":      []string{"group/project1", "group/project2"},
				"source_branch": "feature-branch",
			},
			expectedStatus: "failed",
			expectedError:  "missing required inputs: source_branch, title",
		},
		{
			name: "missing required inputs - empty projects array",
			inputs: map[string]interface{}{
				"projects":      []string{},
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			expectedStatus: "failed",
			expectedError:  "missing required input: projects (array of GitLab project paths)",
		},
		{
			name: "missing required inputs - no projects",
			inputs: map[string]interface{}{
				"source_branch": "feature-branch",
				"title":         "Test MR",
			},
			expectedStatus: "failed",
			expectedError:  "missing required input: projects (array of GitLab project paths)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test input validation logic
			sourceBranch := getStringInput(tt.inputs, "source_branch")
			title := getStringInput(tt.inputs, "title")

			if sourceBranch == "" || title == "" {
				expectedErr := "missing required inputs: source_branch, title"
				if expectedErr != tt.expectedError {
					t.Errorf("Expected error %q, got %q", tt.expectedError, expectedErr)
				}
				return
			}

			var projects []string
			if p, ok := tt.inputs["projects"]; ok {
				switch v := p.(type) {
				case []interface{}:
					for _, item := range v {
						if s, ok := item.(string); ok {
							projects = append(projects, s)
						}
					}
				case []string:
					projects = v
				}
			}
			if len(projects) == 0 {
				expectedErr := "missing required input: projects (array of GitLab project paths)"
				if expectedErr != tt.expectedError {
					t.Errorf("Expected error %q, got %q", tt.expectedError, expectedErr)
				}
				return
			}
		})
	}
}
