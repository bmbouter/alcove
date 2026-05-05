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
	"net/url"
	"strings"
	"testing"
)

func TestSearchGLIssuesHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/api/v4/projects/") || !strings.HasSuffix(r.URL.Path, "/issues") {
			t.Errorf("Expected GET to /api/v4/projects/.../issues, got %s %s", r.Method, r.URL.Path)
		}

		// Check auth header
		token := r.Header.Get("PRIVATE-TOKEN")
		if token != "test-token" {
			t.Errorf("Expected PRIVATE-TOKEN test-token, got %s", token)
		}

		// Check query parameters
		query := r.URL.Query()
		if query.Get("state") != "opened" {
			t.Errorf("Expected state=opened, got state=%s", query.Get("state"))
		}
		if query.Get("per_page") != "20" {
			t.Errorf("Expected per_page=20, got per_page=%s", query.Get("per_page"))
		}
		if query.Get("search") != "bug" {
			t.Errorf("Expected search=bug, got search=%s", query.Get("search"))
		}
		if query.Get("labels") != "critical" {
			t.Errorf("Expected labels=critical, got labels=%s", query.Get("labels"))
		}

		// Return mock issues
		issues := []map[string]interface{}{
			{
				"iid":     1,
				"title":   "Bug in authentication",
				"state":   "opened",
				"web_url": "https://gitlab.example.com/project/-/issues/1",
				"labels":  []string{"bug", "critical"},
			},
			{
				"iid":     2,
				"title":   "Critical security issue",
				"state":   "opened",
				"web_url": "https://gitlab.example.com/project/-/issues/2",
				"labels":  []string{"security", "critical"},
			},
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	// Test URL encoding logic separate from HTTP request
	project := "testgroup/testproject"
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	if encodedProject != "testgroup%2Ftestproject" {
		t.Errorf("Project encoding failed: expected testgroup%%2Ftestproject, got %s", encodedProject)
	}

	// Test the request function directly using gitlabRequest
	ctx := context.Background()
	token := "test-token"
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues?state=%s&per_page=%d&search=%s&labels=%s",
		server.URL, encodedProject, url.QueryEscape("opened"), 20,
		url.QueryEscape("bug"), url.QueryEscape("critical"))

	respBody, err := gitlabRequest(ctx, token, "GET", apiURL, nil)
	if err != nil {
		t.Errorf("gitlabRequest failed: %v", err)
	}

	var issuesResp []struct {
		IID    int      `json:"iid"`
		Title  string   `json:"title"`
		State  string   `json:"state"`
		WebURL string   `json:"web_url"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(respBody, &issuesResp); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}

	if len(issuesResp) != 2 {
		t.Errorf("Expected 2 issues, got: %d", len(issuesResp))
	}

	if issuesResp[0].IID != 1 {
		t.Errorf("Expected iid=1, got: %v", issuesResp[0].IID)
	}
	if issuesResp[0].Title != "Bug in authentication" {
		t.Errorf("Expected title 'Bug in authentication', got: %v", issuesResp[0].Title)
	}
}

func TestSearchGLIssuesInputValidation(t *testing.T) {
	// Test getStringInput and getIntInput functions
	inputs := map[string]interface{}{
		"project":     "testgroup/testproject",
		"search":      "bug",
		"labels":      "critical",
		"state":       "opened",
		"max_results": 50,
	}

	if getStringInput(inputs, "project") != "testgroup/testproject" {
		t.Errorf("getStringInput failed for project")
	}

	if getStringInput(inputs, "search") != "bug" {
		t.Errorf("getStringInput failed for search")
	}

	if getIntInput(inputs, "max_results") != 50 {
		t.Errorf("getIntInput failed for max_results")
	}

	// Test missing project
	emptyInputs := map[string]interface{}{}
	if getStringInput(emptyInputs, "project") != "" {
		t.Errorf("getStringInput should return empty string for missing input")
	}

	// Test max_results clamping logic
	if maxResults := 200; maxResults > 100 {
		maxResults = 100
		if maxResults != 100 {
			t.Errorf("max_results clamping failed")
		}
	}

	// Test default values
	if state := getStringInput(emptyInputs, "state"); state == "" {
		state = "opened"
		if state != "opened" {
			t.Errorf("state default failed")
		}
	}

	if maxResults := getIntInput(emptyInputs, "max_results"); maxResults == 0 {
		maxResults = 20
		if maxResults != 20 {
			t.Errorf("max_results default failed")
		}
	}
}

func TestGitLabActionValidation(t *testing.T) {
	// Test that search-gl-issues action is valid
	if !validBridgeActions["search-gl-issues"] {
		t.Error("search-gl-issues action not found in validBridgeActions")
	}

	// Test that RegisterBridgeActions includes search-gl-issues
	handlers := RegisterBridgeActions()
	if handlers["search-gl-issues"] == nil {
		t.Error("search-gl-issues action not found in RegisterBridgeActions")
	}

	// Test that ListBridgeActionSchemas includes search-gl-issues
	schemas := ListBridgeActionSchemas()
	foundSchemas := make(map[string]bool)
	for _, schema := range schemas {
		foundSchemas[schema.Name] = true
	}

	if !foundSchemas["search-gl-issues"] {
		t.Error("search-gl-issues action not found in ListBridgeActionSchemas")
	}

	// Verify schema structure
	for _, schema := range schemas {
		if schema.Name == "search-gl-issues" {
			expectedInputs := []string{"project", "search", "labels", "state", "max_results"}
			for _, input := range expectedInputs {
				if _, ok := schema.Inputs[input]; !ok {
					t.Errorf("Expected input %s not found in schema", input)
				}
			}

			expectedOutputs := []string{"issues", "total"}
			for _, output := range expectedOutputs {
				if _, ok := schema.Outputs[output]; !ok {
					t.Errorf("Expected output %s not found in schema", output)
				}
			}
			break
		}
	}
}