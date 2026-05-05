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
