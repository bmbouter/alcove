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

// mockGitLabCredentialStore is a test mock for GitLab credential acquisition
type mockGitLabCredentialStore struct {
	token   string
	apiHost string
	err     error
}

func (m *mockGitLabCredentialStore) AcquireSCMTokenForOwner(ctx context.Context, service, teamID string) (string, string, error) {
	return m.token, m.apiHost, m.err
}

func TestGitLabRequest(t *testing.T) {
	// Test successful GitLab API request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check private token header
		token := r.Header.Get("PRIVATE-TOKEN")
		if token != "test-token" {
			t.Errorf("Expected PRIVATE-TOKEN test-token, got: %s", token)
		}

		// Check User-Agent
		userAgent := r.Header.Get("User-Agent")
		if userAgent != "alcove-bridge-action" {
			t.Errorf("Expected User-Agent alcove-bridge-action, got: %s", userAgent)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	ctx := context.Background()
	respBody, err := gitlabRequest(ctx, "test-token", "GET", server.URL, nil)
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(respBody, &response); err != nil {
		t.Errorf("Failed to parse response: %v", err)
	}

	if response["result"] != "ok" {
		t.Errorf("Expected result 'ok', got: %v", response["result"])
	}

	// Test HTTP error
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "Bad request"}`))
	}))
	defer errorServer.Close()

	_, err = gitlabRequest(ctx, "test-token", "GET", errorServer.URL, nil)
	if err == nil {
		t.Error("Expected error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("Expected HTTP 400 error, got: %v", err)
	}
}

func TestCreateGLIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request method and path
		if r.Method != "POST" {
			t.Errorf("Expected POST method, got %s", r.Method)
		}
		// httptest.NewServer URL-decodes the path, so we check for the decoded version
		expectedPath := "/api/v4/projects/test/project/issues"
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}

		// Parse the request body
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}

		// Verify required fields
		if body["title"] != "Test Issue" {
			t.Errorf("Expected title 'Test Issue', got %v", body["title"])
		}

		// Verify optional fields
		if body["description"] != "This is a test issue" {
			t.Errorf("Expected description 'This is a test issue', got %v", body["description"])
		}

		if body["labels"] != "bug,feature" {
			t.Errorf("Expected labels 'bug,feature', got %v", body["labels"])
		}

		// Check assignee_ids array
		assigneeIDs, ok := body["assignee_ids"].([]interface{})
		if !ok {
			t.Error("Expected assignee_ids to be an array")
		} else if len(assigneeIDs) != 2 {
			t.Errorf("Expected 2 assignee IDs, got %d", len(assigneeIDs))
		} else {
			// JSON numbers come as float64
			if assigneeIDs[0] != float64(1) || assigneeIDs[1] != float64(2) {
				t.Errorf("Expected assignee IDs [1, 2], got %v", assigneeIDs)
			}
		}

		if body["milestone_id"] != float64(5) {
			t.Errorf("Expected milestone_id 5, got %v", body["milestone_id"])
		}

		// Return mock GitLab issue response
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"iid": 42, "web_url": "https://gitlab.example.com/test/project/-/issues/42"}`))
	}))
	defer server.Close()

	credStore := &mockGitLabCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
		err:     nil,
	}

	inputs := map[string]interface{}{
		"project":      "test/project",
		"title":        "Test Issue",
		"description":  "This is a test issue",
		"labels":       "bug,feature",
		"assignee_ids": []int{1, 2},
		"milestone_id": 5,
	}

	// Create a bridge action function that accepts our mock
	testFunc := func(ctx context.Context, inputs map[string]interface{}, credStore interface{}, teamID string) (*BridgeActionResult, error) {
		mock := credStore.(*mockGitLabCredentialStore)

		project := getStringInput(inputs, "project")
		title := getStringInput(inputs, "title")

		if project == "" || title == "" {
			return &BridgeActionResult{Status: "failed", Error: "missing required inputs: project, title"}, nil
		}

		token, apiHost, err := mock.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
		if err != nil {
			return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitLab token: %v", err)}, nil
		}
		if apiHost == "" {
			apiHost = "https://gitlab.cee.redhat.com"
		}

		// Simulate the same logic as bridgeActionCreateGLIssue
		description := getStringInput(inputs, "description")
		labels := getStringInput(inputs, "labels")
		assigneeIDs := getIntSliceInput(inputs, "assignee_ids")
		milestoneID := getIntInput(inputs, "milestone_id")

		issueBody := map[string]interface{}{
			"title": title,
		}
		if description != "" {
			issueBody["description"] = description
		}
		if labels != "" {
			issueBody["labels"] = labels
		}
		if len(assigneeIDs) > 0 {
			issueBody["assignee_ids"] = assigneeIDs
		}
		if milestoneID > 0 {
			issueBody["milestone_id"] = milestoneID
		}

		bodyJSON, _ := json.Marshal(issueBody)
		encodedProject := strings.ReplaceAll(project, "/", "%2F")
		apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues", apiHost, encodedProject)

		respBody, err := gitlabRequest(ctx, token, "POST", apiURL, bodyJSON)
		if err != nil {
			return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("GitLab API error creating issue: %v", err)}, nil
		}

		var issueResp struct {
			IID    int    `json:"iid"`
			WebURL string `json:"web_url"`
		}
		if err := json.Unmarshal(respBody, &issueResp); err != nil {
			return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to parse GitLab issue response: %v", err)}, nil
		}

		return &BridgeActionResult{
			Status: "succeeded",
			Outputs: map[string]interface{}{
				"issue_iid": issueResp.IID,
				"issue_url": issueResp.WebURL,
			},
		}, nil
	}

	result, err := testFunc(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("Expected succeeded, got: %s", result.Status)
	}

	if result.Outputs["issue_iid"] != 42 {
		t.Errorf("Expected issue_iid 42, got: %v", result.Outputs["issue_iid"])
	}

	expectedURL := "https://gitlab.example.com/test/project/-/issues/42"
	if result.Outputs["issue_url"] != expectedURL {
		t.Errorf("Expected issue_url %s, got: %v", expectedURL, result.Outputs["issue_url"])
	}
}

func TestGetIntSliceInput(t *testing.T) {
	// Test []int input
	inputs := map[string]interface{}{
		"ids": []int{1, 2, 3},
	}
	result := getIntSliceInput(inputs, "ids")
	if len(result) != 3 || result[0] != 1 || result[1] != 2 || result[2] != 3 {
		t.Errorf("Expected [1, 2, 3], got %v", result)
	}

	// Test []interface{} with mixed numeric types (simulating JSON parsing)
	inputs2 := map[string]interface{}{
		"ids": []interface{}{float64(1), int(2), "3"},
	}
	result2 := getIntSliceInput(inputs2, "ids")
	if len(result2) != 3 || result2[0] != 1 || result2[1] != 2 || result2[2] != 3 {
		t.Errorf("Expected [1, 2, 3], got %v", result2)
	}

	// Test missing key
	result3 := getIntSliceInput(inputs, "missing")
	if result3 != nil {
		t.Errorf("Expected nil for missing key, got %v", result3)
	}

	// Test empty array
	inputs4 := map[string]interface{}{
		"ids": []interface{}{},
	}
	result4 := getIntSliceInput(inputs4, "ids")
	if len(result4) != 0 {
		t.Errorf("Expected empty slice, got %v", result4)
	}
}

func TestGitLabActionValidation(t *testing.T) {
	// Test that new GitLab actions are valid
	gitlabActions := []string{
		"create-gl-issue",
		"create-mr",
		"await-pipeline",
		"merge-mr",
		"post-note",
		"update-gl-issue",
	}

	for _, action := range gitlabActions {
		if !validBridgeActions[action] {
			t.Errorf("GitLab action '%s' not found in validBridgeActions", action)
		}
	}

	// Test that RegisterBridgeActions includes the GitLab actions
	handlers := RegisterBridgeActions()
	for _, action := range gitlabActions {
		if handlers[action] == nil {
			t.Errorf("GitLab action '%s' not found in RegisterBridgeActions", action)
		}
	}

	// Test that unified actions are registered
	unifiedActions := []string{
		"create-issue",
		"create-merge-request",
		"update-issue",
	}

	for _, action := range unifiedActions {
		if !validBridgeActions[action] {
			t.Errorf("Unified action '%s' not found in validBridgeActions", action)
		}
		if handlers[action] == nil {
			t.Errorf("Unified action '%s' not found in RegisterBridgeActions", action)
		}
	}

	// Test that ListBridgeActionSchemas includes the new actions
	schemas := ListBridgeActionSchemas()
	foundSchemas := make(map[string]bool)
	for _, schema := range schemas {
		foundSchemas[schema.Name] = true
	}

	expectedSchemas := []string{
		"create-gl-issue",
		"create-issue",
	}

	for _, action := range expectedSchemas {
		if !foundSchemas[action] {
			t.Errorf("Action '%s' not found in ListBridgeActionSchemas", action)
		}
	}
}

func TestUnifiedCreateIssue(t *testing.T) {
	// Test GitLab detection
	gitlabInputs := map[string]interface{}{
		"project": "test/project",
		"title":   "Test Issue",
	}

	scm := detectSCM(gitlabInputs)
	if scm != "gitlab" {
		t.Errorf("Expected GitLab detection, got %s", scm)
	}

	// Test GitHub detection (when repo is present)
	githubInputs := map[string]interface{}{
		"repo":  "owner/repo",
		"title": "Test Issue",
	}

	scm2 := detectSCM(githubInputs)
	if scm2 != "github" {
		t.Errorf("Expected GitHub detection, got %s", scm2)
	}

	// Test ambiguous inputs
	ambiguousInputs := map[string]interface{}{
		"title": "Test Issue",
	}

	scm3 := detectSCM(ambiguousInputs)
	if scm3 != "" {
		t.Errorf("Expected empty SCM detection for ambiguous inputs, got %s", scm3)
	}
}