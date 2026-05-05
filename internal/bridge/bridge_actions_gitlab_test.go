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

func TestCreateGLIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/api/v4/projects/group%2Fproject/issues"
		if r.Method != "POST" || r.URL.Path != expectedPath {
			t.Errorf("Expected POST to %s, got %s %s", expectedPath, r.Method, r.URL.Path)
		}

		// Check for proper GitLab token header
		authHeader := r.Header.Get("PRIVATE-TOKEN")
		if authHeader != "test-token" {
			t.Errorf("Expected PRIVATE-TOKEN header with test-token, got %s", authHeader)
		}

		// Check request body
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}

		// Check required fields
		if body["title"] != "Test issue" {
			t.Errorf("Expected title 'Test issue', got %v", body["title"])
		}

		// Check optional fields
		if body["description"] != "Test description" {
			t.Errorf("Expected description 'Test description', got %v", body["description"])
		}

		if body["labels"] != "bug,feature" {
			t.Errorf("Expected labels 'bug,feature', got %v", body["labels"])
		}

		// Check assignee_ids as array of integers
		assigneeIDs, ok := body["assignee_ids"].([]interface{})
		if !ok {
			t.Errorf("Expected assignee_ids to be an array, got %T", body["assignee_ids"])
		} else {
			if len(assigneeIDs) != 2 {
				t.Errorf("Expected 2 assignees, got %d", len(assigneeIDs))
			}
			if assigneeIDs[0] != float64(1) || assigneeIDs[1] != float64(2) {
				t.Errorf("Expected assignees [1,2], got %v", assigneeIDs)
			}
		}

		// Check milestone_id
		if body["milestone_id"] != float64(5) {
			t.Errorf("Expected milestone_id 5, got %v", body["milestone_id"])
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"iid": 42, "web_url": "https://gitlab.example.com/group/project/-/issues/42"}`))
	}))
	defer server.Close()

	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
		err:     nil,
	}

	inputs := map[string]interface{}{
		"project":      "group/project",
		"title":        "Test issue",
		"description":  "Test description",
		"labels":       "bug,feature",
		"assignee_ids": []int{1, 2},
		"milestone_id": 5,
	}

	result, err := bridgeActionCreateGLIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("Expected succeeded, got: %s", result.Status)
	}

	if result.Outputs["issue_iid"] != 42 {
		t.Errorf("Expected issue_iid 42, got: %v", result.Outputs["issue_iid"])
	}

	expectedURL := "https://gitlab.example.com/group/project/-/issues/42"
	if result.Outputs["issue_url"] != expectedURL {
		t.Errorf("Expected issue_url %s, got: %v", expectedURL, result.Outputs["issue_url"])
	}
}

func TestCreateGLIssueMissingRequiredInputs(t *testing.T) {
	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: "https://gitlab.example.com",
		err:     nil,
	}

	// Test missing project
	inputs := map[string]interface{}{
		"title": "Test issue",
	}

	result, err := bridgeActionCreateGLIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected failed, got: %s", result.Status)
	}

	if !strings.Contains(result.Error, "missing required inputs: project, title") {
		t.Errorf("Expected missing inputs error, got: %s", result.Error)
	}

	// Test missing title
	inputs = map[string]interface{}{
		"project": "group/project",
	}

	result, err = bridgeActionCreateGLIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected failed, got: %s", result.Status)
	}

	if !strings.Contains(result.Error, "missing required inputs: project, title") {
		t.Errorf("Expected missing inputs error, got: %s", result.Error)
	}
}

func TestCreateGLIssueMinimalInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)

		// Should only have title, no optional fields
		if len(body) != 1 {
			t.Errorf("Expected only 1 field (title), got %d fields: %v", len(body), body)
		}

		if body["title"] != "Minimal issue" {
			t.Errorf("Expected title 'Minimal issue', got %v", body["title"])
		}

		// These should not be present
		if _, hasDesc := body["description"]; hasDesc {
			t.Error("Expected no description field for minimal input")
		}
		if _, hasLabels := body["labels"]; hasLabels {
			t.Error("Expected no labels field for minimal input")
		}

		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"iid": 1, "web_url": "https://gitlab.example.com/group/project/-/issues/1"}`))
	}))
	defer server.Close()

	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
		err:     nil,
	}

	inputs := map[string]interface{}{
		"project": "group/project",
		"title":   "Minimal issue",
	}

	result, err := bridgeActionCreateGLIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("Expected succeeded, got: %s", result.Status)
	}
}

func TestCreateGLIssueAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message": "Title can't be blank"}`))
	}))
	defer server.Close()

	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
		err:     nil,
	}

	inputs := map[string]interface{}{
		"project": "group/project",
		"title":   "Test issue",
	}

	result, err := bridgeActionCreateGLIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected failed, got: %s", result.Status)
	}

	if !strings.Contains(result.Error, "GitLab API error") {
		t.Errorf("Expected GitLab API error, got: %s", result.Error)
	}
}

func TestGetIntSliceInput(t *testing.T) {
	// Test with []int
	inputs := map[string]interface{}{
		"int_slice": []int{1, 2, 3},
	}
	result := getIntSliceInput(inputs, "int_slice")
	expected := []int{1, 2, 3}
	if len(result) != len(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
	for i, v := range result {
		if v != expected[i] {
			t.Errorf("Expected %v, got %v", expected, result)
		}
	}

	// Test with []interface{} containing mixed types
	inputs = map[string]interface{}{
		"mixed_slice": []interface{}{1, 2.0, "3"},
	}
	result = getIntSliceInput(inputs, "mixed_slice")
	expected = []int{1, 2, 3}
	if len(result) != len(expected) {
		t.Errorf("Expected %v, got %v", expected, result)
	}
	for i, v := range result {
		if v != expected[i] {
			t.Errorf("Expected %v, got %v", expected, result)
		}
	}

	// Test with missing key
	result = getIntSliceInput(inputs, "missing")
	if result != nil {
		t.Errorf("Expected nil for missing key, got %v", result)
	}

	// Test with invalid type
	inputs = map[string]interface{}{
		"invalid": "not an array",
	}
	result = getIntSliceInput(inputs, "invalid")
	if result != nil {
		t.Errorf("Expected nil for invalid type, got %v", result)
	}
}

func TestUnifiedCreateIssue(t *testing.T) {
	// Test GitLab detection
	inputs := map[string]interface{}{
		"project": "group/project",
		"title":   "Test issue",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"iid": 1, "web_url": "https://gitlab.example.com/group/project/-/issues/1"}`))
	}))
	defer server.Close()

	credStore := &mockCredentialStore{
		token:   "test-token",
		apiHost: server.URL,
		err:     nil,
	}

	result, err := bridgeActionUnifiedCreateIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "succeeded" {
		t.Errorf("Expected succeeded, got: %s", result.Status)
	}

	// Test GitHub detection (should fail since GitHub side not implemented yet)
	inputs = map[string]interface{}{
		"repo":  "owner/repo",
		"title": "Test issue",
	}

	result, err = bridgeActionUnifiedCreateIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected failed (GitHub not implemented), got: %s", result.Status)
	}

	if !strings.Contains(result.Error, "GitHub issue creation not yet implemented") {
		t.Errorf("Expected GitHub not implemented error, got: %s", result.Error)
	}

	// Test ambiguous inputs
	inputs = map[string]interface{}{
		"title": "Test issue",
	}

	result, err = bridgeActionUnifiedCreateIssue(context.Background(), inputs, credStore, "team1")
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if result.Status != "failed" {
		t.Errorf("Expected failed (cannot detect SCM), got: %s", result.Status)
	}

	if !strings.Contains(result.Error, "cannot detect SCM") {
		t.Errorf("Expected cannot detect SCM error, got: %s", result.Error)
	}
}

func TestGitLabWorkflowValidation(t *testing.T) {
	// Test that new GitLab action names are valid
	gitlabActions := []string{
		"create-gl-issue",
		"create-issue",
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

	// Test that ListBridgeActionSchemas includes the GitLab actions
	schemas := ListBridgeActionSchemas()
	foundSchemas := make(map[string]bool)
	for _, schema := range schemas {
		foundSchemas[schema.Name] = true
	}

	for _, action := range gitlabActions {
		if !foundSchemas[action] {
			t.Errorf("GitLab action '%s' not found in ListBridgeActionSchemas", action)
		}
	}
}