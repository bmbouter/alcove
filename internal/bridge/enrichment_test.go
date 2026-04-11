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

func TestEnrichIssueContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue endpoint
	mux.HandleFunc("/repos/owner/repo/issues/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title": "Test Issue",
			"body":  "This is the issue body.",
			"state": "open",
			"user":  map[string]string{"login": "alice"},
			"labels": []map[string]string{
				{"name": "bug"},
				{"name": "help wanted"},
			},
			"assignees": []map[string]string{
				{"login": "bob"},
			},
		})
	})

	// Mock comments endpoint
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"body":       "First comment here.",
				"user":       map[string]string{"login": "carol"},
				"created_at": "2026-01-15T10:00:00Z",
			},
			{
				"body":       "Second comment.",
				"user":       map[string]string{"login": "dave"},
				"created_at": "2026-01-16T12:00:00Z",
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "issues",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_ISSUE_NUMBER": "1",
		"GITHUB_PR_NUMBER":    "",
		"GITHUB_SHA":          "",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "issues", "opened", meta)

	// Check that the enriched context contains expected data
	checks := []string{
		"## Event Context",
		"**Event**: issues / opened",
		"**Repository**: owner/repo",
		"### Issue #1: Test Issue",
		"**State**: open",
		"**Author**: @alice",
		"**Labels**: bug, help wanted",
		"**Assignees**: @bob",
		"This is the issue body.",
		"### Comments (2)",
		"**@carol** (2026-01-15):",
		"First comment here.",
		"**@dave** (2026-01-16):",
		"Second comment.",
		"---",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("enriched context missing expected content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichPRContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock PR endpoint
	mux.HandleFunc("/repos/owner/repo/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":  "Add new feature",
			"body":   "PR description here.",
			"state":  "open",
			"merged": false,
			"user":   map[string]string{"login": "alice"},
			"head": map[string]interface{}{
				"ref": "feature-branch",
				"sha": "abc123def456",
			},
			"base": map[string]interface{}{
				"ref": "main",
			},
			"labels": []map[string]string{
				{"name": "enhancement"},
			},
		})
	})

	// Mock reviews endpoint
	mux.HandleFunc("/repos/owner/repo/pulls/42/reviews", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"state": "CHANGES_REQUESTED",
				"body":  "Please fix the tests.",
				"user":  map[string]string{"login": "reviewer1"},
			},
			{
				"state": "APPROVED",
				"body":  "",
				"user":  map[string]string{"login": "reviewer2"},
			},
		})
	})

	// Mock check-runs endpoint
	mux.HandleFunc("/repos/owner/repo/commits/abc123def456/check-runs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"check_runs": []map[string]interface{}{
				{"name": "tests", "conclusion": "failure", "status": "completed"},
				{"name": "lint", "conclusion": "success", "status": "completed"},
				{"name": "build", "conclusion": "", "status": "in_progress"},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "pull_request",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_PR_NUMBER":    "42",
		"GITHUB_ISSUE_NUMBER": "",
		"GITHUB_SHA":          "abc123def456",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "pull_request", "synchronize", meta)

	checks := []string{
		"## Event Context",
		"**Event**: pull_request / synchronize",
		"**Repository**: owner/repo",
		"### PR #42: Add new feature",
		"**State**: open (merged: false)",
		"**Branch**: feature-branch → main",
		"**Author**: @alice",
		"**Head SHA**: abc123def456",
		"**Labels**: enhancement",
		"PR description here.",
		"### Reviews",
		"**@reviewer1** (CHANGES_REQUESTED):",
		"Please fix the tests.",
		"### CI Status",
		"- tests (failure)",
		"- lint (success)",
		"- build (in_progress)",
		"---",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("enriched context missing expected content: %q\n\nFull result:\n%s", check, result)
		}
	}

	// Reviews with empty body should be skipped
	if strings.Contains(result, "@reviewer2") {
		t.Errorf("review with empty body should not appear in output")
	}
}

func TestEnrichTruncatesLongBodies(t *testing.T) {
	mux := http.NewServeMux()

	longBody := strings.Repeat("x", maxBodyLen+500)

	mux.HandleFunc("/repos/owner/repo/issues/5", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title": "Long Issue",
			"body":  longBody,
			"state": "open",
			"user":  map[string]string{"login": "alice"},
		})
	})

	longComment := strings.Repeat("y", maxCommentLen+500)
	mux.HandleFunc("/repos/owner/repo/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"body":       longComment,
				"user":       map[string]string{"login": "bob"},
				"created_at": "2026-01-20T10:00:00Z",
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "issues",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_ISSUE_NUMBER": "5",
		"GITHUB_PR_NUMBER":    "",
		"GITHUB_SHA":          "",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "issues", "opened", meta)

	// Body should be truncated
	if !strings.Contains(result, "... (truncated)") {
		t.Error("expected truncation marker in output")
	}

	// Full long body should NOT appear
	if strings.Contains(result, longBody) {
		t.Error("full long body should not appear in output")
	}

	// Full long comment should NOT appear
	if strings.Contains(result, longComment) {
		t.Error("full long comment should not appear in output")
	}
}

func TestEnrichGracefulAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 404 for issue
	mux.HandleFunc("/repos/owner/repo/issues/999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "issues",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_ISSUE_NUMBER": "999",
		"GITHUB_PR_NUMBER":    "",
		"GITHUB_SHA":          "",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "issues", "opened", meta)

	// Should not panic and should contain error info
	if !strings.Contains(result, "Could not fetch issue #999") {
		t.Errorf("expected error message for 404, got:\n%s", result)
	}

	// Should still have the event context header
	if !strings.Contains(result, "## Event Context") {
		t.Error("missing event context header")
	}
}

func TestEnrichUnknownEventType(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "push",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_ISSUE_NUMBER": "",
		"GITHUB_PR_NUMBER":    "",
		"GITHUB_SHA":          "abc123",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "push", "completed", meta)

	if !strings.Contains(result, "No additional context available for this event type.") {
		t.Errorf("expected 'no additional context' message for push event, got:\n%s", result)
	}

	if !strings.Contains(result, "## Event Context") {
		t.Error("missing event context header")
	}
	if !strings.Contains(result, "**Event**: push / completed") {
		t.Error("missing event type in output")
	}
}

func TestEnrichLabelAdded(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "push",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_ISSUE_NUMBER": "",
		"GITHUB_PR_NUMBER":    "",
		"GITHUB_SHA":          "",
		"GITHUB_LABEL_ADDED":  "ready-for-dev",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "push", "labeled", meta)

	if !strings.Contains(result, "**Label Added**: ready-for-dev") {
		t.Errorf("expected label added line, got:\n%s", result)
	}
}

func TestEnrichPRContextAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 404 for PR
	mux.HandleFunc("/repos/owner/repo/pulls/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}

	meta := map[string]string{
		"GITHUB_EVENT":        "pull_request",
		"GITHUB_REPO":         "owner/repo",
		"GITHUB_PR_NUMBER":    "99",
		"GITHUB_ISSUE_NUMBER": "",
		"GITHUB_SHA":          "",
		"GITHUB_LABEL_ADDED":  "",
	}

	result := poller.enrichEventContext(context.Background(), "fake-token", ts.URL, "pull_request", "opened", meta)

	if !strings.Contains(result, "Could not fetch PR #99") {
		t.Errorf("expected error message for 404 PR, got:\n%s", result)
	}
}

func TestGithubAPIGetAuthHeader(t *testing.T) {
	var capturedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer ts.Close()

	poller := &GitHubPoller{client: ts.Client()}
	_, err := poller.githubAPIGet(context.Background(), "test-token-123", ts.URL+"/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuth != "token test-token-123" {
		t.Errorf("expected 'token test-token-123', got %q", capturedAuth)
	}
}
