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

func TestEnrichGitLabMRContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR endpoint
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/42", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Add new feature",
			"description":   "MR description here.",
			"state":         "opened",
			"source_branch": "feature-branch",
			"target_branch": "main",
			"author":        map[string]string{"username": "alice"},
			"labels":        []string{"enhancement", "ready-for-review"},
			"web_url":       "https://gitlab.example.com/group/repo/-/merge_requests/42",
			"sha":           "abc123def456",
		})
	})

	// Mock notes endpoint
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/42/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"body":       "First comment on MR.",
				"author":     map[string]string{"username": "bob"},
				"created_at": "2026-01-15T10:00:00Z",
				"system":     false,
			},
			{
				"body":       "System note: merged",
				"author":     map[string]string{"username": "gitlab"},
				"created_at": "2026-01-16T12:00:00Z",
				"system":     true,
			},
			{
				"body":       "Second user comment.",
				"author":     map[string]string{"username": "carol"},
				"created_at": "2026-01-16T14:00:00Z",
				"system":     false,
			},
		})
	})

	// Mock pipelines endpoint
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/42/pipelines", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":      12345,
				"status":  "success",
				"web_url": "https://gitlab.example.com/group/repo/-/pipelines/12345",
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "group/repo",
		"GITLAB_MR_IID":      "42",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	// Check that the enriched context contains expected data
	checks := []string{
		"## Event Context",
		"**Event**: merge_request / opened",
		"**Project**: group/repo",
		"### MR !42: Add new feature",
		"**State**: opened",
		"**Author**: @alice",
		"**Branch**: feature-branch → main",
		"**SHA**: abc123def456",
		"**Labels**: enhancement, ready-for-review",
		"MR description here.",
		"### Notes (2)", // Should only count non-system notes
		"**@bob** (2026-01-15):",
		"First comment on MR.",
		"**@carol** (2026-01-16):",
		"Second user comment.",
		"### Pipeline Status",
		"- Pipeline #12345 (success)",
		"---",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("enriched context missing expected content: %q\n\nFull result:\n%s", check, result)
		}
	}

	// Should NOT contain system notes
	if strings.Contains(result, "System note: merged") {
		t.Error("system note should not appear in output")
	}
}

func TestEnrichGitLabIssueContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue endpoint
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Test Issue",
			"description": "This is the issue description.",
			"state":       "opened",
			"author":      map[string]string{"username": "alice"},
			"labels":      []string{"bug", "help wanted"},
			"assignees": []map[string]string{
				{"username": "bob"},
				{"username": "carol"},
			},
			"web_url": "https://gitlab.example.com/owner/repo/-/issues/123",
		})
	})

	// Mock notes endpoint
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/123/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"body":       "Issue comment here.",
				"author":     map[string]string{"username": "dave"},
				"created_at": "2026-01-15T10:00:00Z",
				"system":     false,
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "123",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "issue", "opened", meta)

	checks := []string{
		"## Event Context",
		"**Event**: issue / opened",
		"**Project**: owner/repo",
		"### Issue #123: Test Issue",
		"**State**: opened",
		"**Author**: @alice",
		"**Labels**: bug, help wanted",
		"**Assignees**: @bob, @carol",
		"This is the issue description.",
		"### Notes (1)",
		"**@dave** (2026-01-15):",
		"Issue comment here.",
		"---",
	}
	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("enriched context missing expected content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichGitLabTruncatesLongBodies(t *testing.T) {
	mux := http.NewServeMux()

	longDescription := strings.Repeat("x", maxBodyLen+500)

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/456", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Long Issue",
			"description": longDescription,
			"state":       "opened",
			"author":      map[string]string{"username": "alice"},
		})
	})

	longNote := strings.Repeat("y", maxCommentLen+500)
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/456/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"body":       longNote,
				"author":     map[string]string{"username": "bob"},
				"created_at": "2026-01-20T10:00:00Z",
				"system":     false,
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "456",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "issue", "opened", meta)

	// Description should be truncated
	if !strings.Contains(result, "... (truncated)") {
		t.Error("expected truncation marker in output")
	}

	// Full long description should NOT appear
	if strings.Contains(result, longDescription) {
		t.Error("full long description should not appear in output")
	}

	// Full long note should NOT appear
	if strings.Contains(result, longNote) {
		t.Error("full long note should not appear in output")
	}
}

func TestEnrichGitLabGracefulAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 404 for issue
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/999", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "999",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "issue", "opened", meta)

	// Should not panic and should contain error info
	if !strings.Contains(result, "Could not fetch issue #999") {
		t.Errorf("expected error message for 404, got:\n%s", result)
	}

	// Should still have the event context header
	if !strings.Contains(result, "## Event Context") {
		t.Error("missing event context header")
	}
}

func TestEnrichGitLabUnknownEventType(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "push", "completed", meta)

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

func TestEnrichGitLabLabelAdded(t *testing.T) {
	ts := httptest.NewServer(http.NewServeMux())
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "ready-for-dev",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "push", "labeled", meta)

	if !strings.Contains(result, "**Label Added**: ready-for-dev") {
		t.Errorf("expected label added line, got:\n%s", result)
	}
}

func TestEnrichGitLabMRAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 404 for MR
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/merge_requests/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "99",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	if !strings.Contains(result, "Could not fetch MR !99") {
		t.Errorf("expected error message for 404 MR, got:\n%s", result)
	}
}

func TestGitLabAPIGetAuthHeader(t *testing.T) {
	var capturedAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())
	_, err := enricher.gitlabAPIGet(context.Background(), "test-token-123", ts.URL+"/test")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedAuth != "test-token-123" {
		t.Errorf("expected 'test-token-123', got %q", capturedAuth)
	}
}

func TestExtractGitLabIssueContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue endpoint
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/789", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Test Context Issue",
			"description": "Context extraction test.",
			"state":       "closed",
			"author":      map[string]string{"username": "alice"},
			"labels":      []string{"enhancement", "documentation"},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	result := enricher.ExtractGitLabIssueContext(context.Background(), "fake-token", ts.URL, "owner/repo", "789")

	if result == nil {
		t.Fatal("expected non-nil context result")
	}

	expected := map[string]interface{}{
		"issue_id":          "789",
		"issue_title":       "Test Context Issue",
		"issue_description": "Context extraction test.",
		"issue_state":       "closed",
		"issue_author":      "alice",
		"issue_labels":      "enhancement,documentation",
		"project":           "owner/repo",
	}

	for key, expectedValue := range expected {
		if result[key] != expectedValue {
			t.Errorf("expected %s=%v, got %v", key, expectedValue, result[key])
		}
	}
}

func TestExtractGitLabIssueContextAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 500 for issue
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/500", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"Internal Server Error"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	result := enricher.ExtractGitLabIssueContext(context.Background(), "fake-token", ts.URL, "owner/repo", "500")

	if result != nil {
		t.Errorf("expected nil context result for API error, got %v", result)
	}
}

func TestEnrichGitLabProjectEncoding(t *testing.T) {
	mux := http.NewServeMux()

	// Test with project path that needs URL encoding
	mux.HandleFunc("/api/v4/projects/my-group%2Fsub-group%2Fmy-project/issues/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Encoding Test",
			"description": "Test project path encoding.",
			"state":       "opened",
			"author":      map[string]string{"username": "alice"},
		})
	})

	mux.HandleFunc("/api/v4/projects/my-group%2Fsub-group%2Fmy-project/issues/1/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "my-group/sub-group/my-project",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "1",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "issue", "opened", meta)

	if !strings.Contains(result, "### Issue #1: Encoding Test") {
		t.Errorf("expected issue title to appear, got:\n%s", result)
	}
}

func TestEnrichGitLabEmptyDescriptionFields(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue with empty description
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/empty", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Empty Description",
			"description": "",
			"state":       "opened",
			"author":      map[string]string{"username": "alice"},
		})
	})

	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/empty/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "owner/repo",
		"GITLAB_MR_IID":      "",
		"GITLAB_ISSUE_IID":   "empty",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "issue", "opened", meta)

	if !strings.Contains(result, "(empty)") {
		t.Error("expected '(empty)' placeholder for empty description")
	}
}

func TestEnrichGitLabMRDiffStats(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR endpoint
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Test MR with diff stats",
			"description":   "MR with changes",
			"state":         "opened",
			"source_branch": "feature",
			"target_branch": "main",
			"author":        map[string]string{"username": "dev"},
			"labels":        []string{"feature"},
			"assignees":     []map[string]string{{"username": "reviewer"}},
		})
	})

	// Mock MR changes endpoint (diff stats)
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/123/changes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"changes_count": "3",
			"changes": []map[string]interface{}{
				{
					"old_path":     "file1.go",
					"new_path":     "file1.go",
					"deleted_file": false,
					"new_file":     false,
					"diff":         "@@ -1,3 +1,5 @@\n func test() {\n+\tlog.Println(\"added\")\n+\tlog.Println(\"another addition\")\n \treturn\n }",
				},
				{
					"old_path":     "file2.txt",
					"new_path":     "file2.txt",
					"deleted_file": true,
					"new_file":     false,
					"diff":         "@@ -1,2 +0,0 @@\n-line1\n-line2",
				},
				{
					"old_path":     "",
					"new_path":     "file3.md",
					"deleted_file": false,
					"new_file":     true,
					"diff":         "@@ -0,0 +1,1 @@\n+# New file",
				},
			},
		})
	})

	// Mock empty notes and pipelines
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/123/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/123/pipelines", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	// Mock approvals (return 404 to simulate GitLab CE)
	mux.HandleFunc("/api/v4/projects/group%2Frepo/merge_requests/123/approvals", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "group/repo",
		"GITLAB_MR_IID":      "123",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	// Check diff stats section
	if !strings.Contains(result, "### Changed Files") {
		t.Error("expected changed files section")
	}
	if !strings.Contains(result, "**Changed files**: 3 (+3, -2)") {
		t.Error("expected files count and line stats")
	}
	if !strings.Contains(result, "file1.go") {
		t.Error("expected file1.go in file list")
	}
	if !strings.Contains(result, "file2.txt (deleted)") {
		t.Error("expected file2.txt marked as deleted")
	}
	if !strings.Contains(result, "file3.md (new)") {
		t.Error("expected file3.md marked as new")
	}

	// Should show assignees
	if !strings.Contains(result, "**Assignees**: @reviewer") {
		t.Error("expected assignees section")
	}
}

func TestEnrichGitLabPipelineJobsWithFailures(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR endpoint
	mux.HandleFunc("/api/v4/projects/org%2Fproject/merge_requests/456", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Test CI",
			"description":   "Testing CI enrichment",
			"state":         "opened",
			"source_branch": "ci-test",
			"target_branch": "main",
			"author":        map[string]string{"username": "dev"},
		})
	})

	// Mock pipelines endpoint
	mux.HandleFunc("/api/v4/projects/org%2Fproject/merge_requests/456/pipelines", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":      789,
				"status":  "failed",
				"web_url": "https://gitlab.example.com/org/project/-/pipelines/789",
			},
		})
	})

	// Mock pipeline jobs
	mux.HandleFunc("/api/v4/projects/org%2Fproject/pipelines/789/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":      1001,
				"name":    "test",
				"status":  "success",
				"stage":   "test",
				"web_url": "https://gitlab.example.com/org/project/-/jobs/1001",
			},
			{
				"id":      1002,
				"name":    "build",
				"status":  "failed",
				"stage":   "build",
				"web_url": "https://gitlab.example.com/org/project/-/jobs/1002",
			},
			{
				"id":      1003,
				"name":    "lint",
				"status":  "success",
				"stage":   "test",
				"web_url": "https://gitlab.example.com/org/project/-/jobs/1003",
			},
		})
	})

	// Mock job trace for failed job
	mux.HandleFunc("/api/v4/projects/org%2Fproject/jobs/1002/trace", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Build failed: compilation error\nError: undefined variable 'foo'\nExit code: 1")
	})

	// Mock empty endpoints
	mux.HandleFunc("/api/v4/projects/org%2Fproject/merge_requests/456/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	mux.HandleFunc("/api/v4/projects/org%2Fproject/merge_requests/456/changes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"changes": []map[string]interface{}{},
		})
	})
	mux.HandleFunc("/api/v4/projects/org%2Fproject/merge_requests/456/approvals", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "org/project",
		"GITLAB_MR_IID":      "456",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	// Check CI Status section
	if !strings.Contains(result, "### CI Status") {
		t.Error("expected CI Status section")
	}
	if !strings.Contains(result, "**Pipeline #789** (failed)") {
		t.Error("expected pipeline status")
	}

	// Check jobs grouped by stage
	if !strings.Contains(result, "**test:**") {
		t.Error("expected test stage")
	}
	if !strings.Contains(result, "**build:**") {
		t.Error("expected build stage")
	}
	if !strings.Contains(result, "test (success)") {
		t.Error("expected test job status")
	}
	if !strings.Contains(result, "build (failed)") {
		t.Error("expected build job status")
	}
	if !strings.Contains(result, "lint (success)") {
		t.Error("expected lint job status")
	}

	// Check failure log
	if !strings.Contains(result, "**build failure log:**") {
		t.Error("expected build failure log section")
	}
	if !strings.Contains(result, "Build failed: compilation error") {
		t.Error("expected failure log content")
	}
	if !strings.Contains(result, "undefined variable 'foo'") {
		t.Error("expected specific error message")
	}
}

func TestEnrichGitLabMRApprovals(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR endpoint
	mux.HandleFunc("/api/v4/projects/enterprise%2Fproject/merge_requests/789", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Enterprise MR",
			"description":   "MR with approvals",
			"state":         "opened",
			"source_branch": "feature",
			"target_branch": "main",
			"author":        map[string]string{"username": "dev"},
		})
	})

	// Mock MR approvals (GitLab Premium feature)
	mux.HandleFunc("/api/v4/projects/enterprise%2Fproject/merge_requests/789/approvals", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"approvals_required": 2,
			"approvals_left":     1,
			"approved_by": []map[string]interface{}{
				{
					"user": map[string]string{"username": "lead"},
				},
			},
			"suggested_approvers": []map[string]string{
				{"username": "architect"},
				{"username": "security"},
			},
		})
	})

	// Mock empty endpoints
	mux.HandleFunc("/api/v4/projects/enterprise%2Fproject/merge_requests/789/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	mux.HandleFunc("/api/v4/projects/enterprise%2Fproject/merge_requests/789/changes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"changes": []map[string]interface{}{},
		})
	})
	mux.HandleFunc("/api/v4/projects/enterprise%2Fproject/merge_requests/789/pipelines", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "enterprise/project",
		"GITLAB_MR_IID":      "789",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	// Check Approvals section
	if !strings.Contains(result, "### Approvals") {
		t.Error("expected Approvals section")
	}
	if !strings.Contains(result, "**Required approvals**: 2") {
		t.Error("expected required approvals count")
	}
	if !strings.Contains(result, "**Approvals remaining**: 1") {
		t.Error("expected remaining approvals count")
	}
	if !strings.Contains(result, "**Approved by**: @lead") {
		t.Error("expected approved by list")
	}
	if !strings.Contains(result, "**Suggested approvers**: @architect, @security") {
		t.Error("expected suggested approvers list")
	}
}

func TestExtractGitLabMRContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR endpoint
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/merge_requests/42", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Feature MR",
			"description":   "Adds new feature to the app.",
			"state":         "opened",
			"source_branch": "feature-branch",
			"target_branch": "develop",
			"author":        map[string]string{"username": "contributor"},
			"labels":        []string{"enhancement", "needs-review"},
			"assignees": []map[string]string{
				{"username": "maintainer"},
				{"username": "reviewer"},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	result := enricher.ExtractGitLabMRContext(context.Background(), "fake-token", ts.URL, "owner/repo", "42")

	if result == nil {
		t.Fatal("expected non-nil MR context result")
	}

	expected := map[string]interface{}{
		"mr_iid":           "42",
		"mr_title":         "Feature MR",
		"mr_description":   "Adds new feature to the app.",
		"mr_body":          "Adds new feature to the app.",
		"mr_state":         "opened",
		"mr_author":        "contributor",
		"mr_source_branch": "feature-branch",
		"mr_target_branch": "develop",
		"mr_labels":        "enhancement,needs-review",
		"mr_assignees":     "maintainer,reviewer",
		"project":          "owner/repo",
	}

	for key, expectedValue := range expected {
		if result[key] != expectedValue {
			t.Errorf("expected %s=%v, got %v", key, expectedValue, result[key])
		}
	}
}

func TestExtractGitLabMRContextAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 403 for MR
	mux.HandleFunc("/api/v4/projects/private%2Frepo/merge_requests/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Forbidden"}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	result := enricher.ExtractGitLabMRContext(context.Background(), "fake-token", ts.URL, "private/repo", "99")

	if result != nil {
		t.Errorf("expected nil MR context result for API error, got %v", result)
	}
}

func TestExtractGitLabIssueContextEnhanced(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue endpoint with assignees
	mux.HandleFunc("/api/v4/projects/owner%2Frepo/issues/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":       "Enhanced Issue",
			"description": "Issue with assignees.",
			"state":       "opened",
			"author":      map[string]string{"username": "reporter"},
			"labels":      []string{"bug", "priority-high"},
			"assignees": []map[string]string{
				{"username": "dev1"},
				{"username": "dev2"},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	result := enricher.ExtractGitLabIssueContext(context.Background(), "fake-token", ts.URL, "owner/repo", "123")

	if result == nil {
		t.Fatal("expected non-nil issue context result")
	}

	expected := map[string]interface{}{
		"issue_id":          "123",
		"issue_title":       "Enhanced Issue",
		"issue_description": "Issue with assignees.",
		"issue_body":        "Issue with assignees.",
		"issue_state":       "opened",
		"issue_author":      "reporter",
		"issue_assignees":   "dev1,dev2",
		"issue_labels":      "bug,priority-high",
		"project":           "owner/repo",
	}

	for key, expectedValue := range expected {
		if result[key] != expectedValue {
			t.Errorf("expected %s=%v, got %v", key, expectedValue, result[key])
		}
	}
}

func TestEnrichGitLabJobTraceWithBinaryContent(t *testing.T) {
	mux := http.NewServeMux()

	// Mock MR and pipeline setup
	mux.HandleFunc("/api/v4/projects/test%2Frepo/merge_requests/1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":         "Test Binary Logs",
			"description":   "Test handling of binary log content",
			"state":         "opened",
			"source_branch": "test",
			"target_branch": "main",
			"author":        map[string]string{"username": "dev"},
		})
	})

	mux.HandleFunc("/api/v4/projects/test%2Frepo/merge_requests/1/pipelines", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":     100,
				"status": "failed",
			},
		})
	})

	mux.HandleFunc("/api/v4/projects/test%2Frepo/pipelines/100/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{
			{
				"id":     200,
				"name":   "failing-job",
				"status": "failed",
				"stage":  "test",
			},
		})
	})

	// Mock job trace with binary content type
	mux.HandleFunc("/api/v4/projects/test%2Frepo/jobs/200/trace", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		// Write some binary content
		w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	})

	// Mock empty endpoints
	mux.HandleFunc("/api/v4/projects/test%2Frepo/merge_requests/1/notes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]interface{}{})
	})
	mux.HandleFunc("/api/v4/projects/test%2Frepo/merge_requests/1/changes", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"changes": []map[string]interface{}{}})
	})
	mux.HandleFunc("/api/v4/projects/test%2Frepo/merge_requests/1/approvals", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	enricher := NewGitLabEnricher(ts.Client())

	meta := map[string]string{
		"GITLAB_PROJECT":     "test/repo",
		"GITLAB_MR_IID":      "1",
		"GITLAB_ISSUE_IID":   "",
		"GITLAB_LABEL_ADDED": "",
	}

	result := enricher.EnrichGitLabEventContext(context.Background(), "fake-token", ts.URL, "merge_request", "opened", meta)

	// Should have CI status section but no failure log for binary content
	if !strings.Contains(result, "### CI Status") {
		t.Error("expected CI Status section")
	}
	if !strings.Contains(result, "failing-job (failed)") {
		t.Error("expected failed job status")
	}
	// Should NOT contain failure log section since content is binary
	if strings.Contains(result, "**failing-job failure log:**") {
		t.Error("should not show failure log for binary content")
	}
}