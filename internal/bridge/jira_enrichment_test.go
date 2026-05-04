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
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEnrichJiraIssueContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"key": "TEST-123",
				"fields": {
					"summary": "Test Issue",
					"description": "This is a test issue description",
					"status": {"name": "Open"},
					"issuetype": {"name": "Bug"},
					"priority": {"name": "High"},
					"assignee": {"displayName": "John Doe"},
					"reporter": {"displayName": "Jane Smith"},
					"components": [{"name": "Backend"}],
					"labels": ["urgent", "bug"],
					"issuelinks": [{
						"type": {"name": "Blocks"},
						"outwardIssue": {
							"key": "TEST-124",
							"fields": {"summary": "Related Issue"}
						}
					}],
					"attachment": [{
						"filename": "test.log",
						"size": 2048,
						"mimeType": "text/plain"
					}]
				}
			}`))
		case strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123/comment"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"comments": [{
					"body": "This is a test comment",
					"author": {"displayName": "Bob Wilson"},
					"created": "2023-05-01T10:00:00Z"
				}]
			}`))
		case strings.Contains(r.URL.Path, "/rest/agile/1.0/issue/TEST-123"):
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"fields": {
					"sprint": {
						"name": "Sprint 1",
						"state": "ACTIVE"
					}
				}
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	ctx := context.Background()
	markdown, enrichData, err := jp.enrichJiraIssueContext(ctx, "test-token", "TEST-123")

	if err != nil {
		t.Fatalf("enrichJiraIssueContext failed: %v", err)
	}

	// Check markdown structure
	if !strings.Contains(markdown, "## Event Context") {
		t.Error("Missing event context header")
	}
	if !strings.Contains(markdown, "### Issue TEST-123: Test Issue") {
		t.Error("Missing issue header")
	}
	if !strings.Contains(markdown, "**Status**: Open") {
		t.Error("Missing status")
	}
	if !strings.Contains(markdown, "**Priority**: High") {
		t.Error("Missing priority")
	}
	if !strings.Contains(markdown, "**Assignee**: John Doe") {
		t.Error("Missing assignee")
	}
	if !strings.Contains(markdown, "### Comments (1)") {
		t.Error("Missing comments section")
	}
	if !strings.Contains(markdown, "**Bob Wilson**") {
		t.Error("Missing comment author")
	}
	if !strings.Contains(markdown, "### Linked Issues (1)") {
		t.Error("Missing linked issues section")
	}
	if !strings.Contains(markdown, "### Sprint") {
		t.Error("Missing sprint section")
	}
	if !strings.Contains(markdown, "### Attachments (1)") {
		t.Error("Missing attachments section")
	}

	// Check enrichment data
	if enrichData == nil {
		t.Fatal("enrichData is nil")
	}
	if enrichData.issue.Key != "TEST-123" {
		t.Errorf("Expected issue key TEST-123, got %s", enrichData.issue.Key)
	}
	if len(enrichData.comments) != 1 {
		t.Errorf("Expected 1 comment, got %d", len(enrichData.comments))
	}
	if enrichData.sprint == nil {
		t.Error("Expected sprint data, got nil")
	}
}

func TestEnrichJiraComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/comment") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check query parameters
		params, _ := url.ParseQuery(r.URL.RawQuery)
		if params.Get("maxResults") != "20" {
			t.Errorf("Expected maxResults=20, got %s", params.Get("maxResults"))
		}
		if params.Get("orderBy") != "-created" {
			t.Errorf("Expected orderBy=-created, got %s", params.Get("orderBy"))
		}

		longComment := strings.Repeat("This is a very long comment. ", 100) // > maxCommentLen
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"comments": [
				{
					"body": "Short comment",
					"author": {"displayName": "User 1"},
					"created": "2023-05-01T10:00:00Z"
				},
				{
					"body": "` + longComment + `",
					"author": {"displayName": "User 2"},
					"created": "2023-05-02T11:00:00Z"
				}
			]
		}`))
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	var sb strings.Builder
	ctx := context.Background()
	comments := jp.enrichIssueComments(ctx, "test-token", "TEST-123", &sb)

	output := sb.String()

	// Check comments structure
	if !strings.Contains(output, "### Comments (2)") {
		t.Error("Missing comments header")
	}
	if !strings.Contains(output, "**User 1** (2023-05-01):") {
		t.Error("Missing first comment header")
	}
	if !strings.Contains(output, "Short comment") {
		t.Error("Missing short comment content")
	}
	if !strings.Contains(output, "**User 2** (2023-05-02):") {
		t.Error("Missing second comment header")
	}
	if !strings.Contains(output, "... (truncated)") {
		t.Error("Long comment should be truncated")
	}

	// Check returned comments
	if len(comments) != 2 {
		t.Errorf("Expected 2 comments, got %d", len(comments))
	}
}

func TestEnrichJiraLinkedIssues(t *testing.T) {
	issue := &jiraIssue{
		Key: "TEST-123",
		Fields: struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee *struct {
				DisplayName string `json:"displayName"`
			} `json:"assignee"`
			Reporter *struct {
				DisplayName string `json:"displayName"`
			} `json:"reporter"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			Labels     []string `json:"labels"`
			IssueLinks []struct {
				Type struct {
					Name string `json:"name"`
				} `json:"type"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			IssueLinks: []struct {
				Type struct {
					Name string `json:"name"`
				} `json:"type"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			}{
				{
					Type: struct {
						Name string `json:"name"`
					}{Name: "Blocks"},
					OutwardIssue: &struct {
						Key    string `json:"key"`
						Fields struct {
							Summary string `json:"summary"`
						} `json:"fields"`
					}{
						Key: "TEST-124",
						Fields: struct {
							Summary string `json:"summary"`
						}{Summary: "Blocked Issue"},
					},
				},
				{
					Type: struct {
						Name string `json:"name"`
					}{Name: "Relates"},
					InwardIssue: &struct {
						Key    string `json:"key"`
						Fields struct {
							Summary string `json:"summary"`
						} `json:"fields"`
					}{
						Key: "TEST-125",
						Fields: struct {
							Summary string `json:"summary"`
						}{Summary: "Related Issue"},
					},
				},
			},
		},
	}

	jp := &JiraPoller{}
	var sb strings.Builder
	jp.enrichLinkedIssues(issue, &sb)

	output := sb.String()

	if !strings.Contains(output, "### Linked Issues (2)") {
		t.Error("Missing linked issues header")
	}
	if !strings.Contains(output, "[Blocks] TEST-124: Blocked Issue") {
		t.Error("Missing outward issue")
	}
	if !strings.Contains(output, "[Relates] TEST-125: Related Issue") {
		t.Error("Missing inward issue")
	}
}

func TestEnrichJiraSprintContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/rest/agile/1.0/issue/TEST-123") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Check query parameters
		params, _ := url.ParseQuery(r.URL.RawQuery)
		if params.Get("fields") != "sprint" {
			t.Errorf("Expected fields=sprint, got %s", params.Get("fields"))
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"fields": {
				"sprint": {
					"name": "Sprint 1",
					"state": "ACTIVE"
				}
			}
		}`))
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	var sb strings.Builder
	ctx := context.Background()
	sprint := jp.enrichSprintContext(ctx, "test-token", "TEST-123", &sb)

	output := sb.String()

	if !strings.Contains(output, "### Sprint") {
		t.Error("Missing sprint header")
	}
	if !strings.Contains(output, "**Name**: Sprint 1") {
		t.Error("Missing sprint name")
	}
	if !strings.Contains(output, "**State**: ACTIVE") {
		t.Error("Missing sprint state")
	}

	if sprint == nil {
		t.Fatal("Expected sprint data, got nil")
	}
	if sprint.Name != "Sprint 1" {
		t.Errorf("Expected sprint name 'Sprint 1', got '%s'", sprint.Name)
	}
	if sprint.State != "ACTIVE" {
		t.Errorf("Expected sprint state 'ACTIVE', got '%s'", sprint.State)
	}
}

func TestEnrichJiraAttachmentMetadata(t *testing.T) {
	issue := &jiraIssue{
		Fields: struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee *struct {
				DisplayName string `json:"displayName"`
			} `json:"assignee"`
			Reporter *struct {
				DisplayName string `json:"displayName"`
			} `json:"reporter"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			Labels     []string `json:"labels"`
			IssueLinks []struct {
				Type struct {
					Name string `json:"name"`
				} `json:"type"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			Attachment: []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			}{
				{Filename: "test.log", Size: 2048, MimeType: "text/plain"},
				{Filename: "screenshot.png", Size: 1048576, MimeType: "image/png"},
			},
		},
	}

	jp := &JiraPoller{}
	var sb strings.Builder
	jp.enrichAttachments(issue, &sb)

	output := sb.String()

	if !strings.Contains(output, "### Attachments (2)") {
		t.Error("Missing attachments header")
	}
	if !strings.Contains(output, "test.log (2 KB, text/plain)") {
		t.Error("Missing first attachment")
	}
	if !strings.Contains(output, "screenshot.png (1024 KB, image/png)") {
		t.Error("Missing second attachment")
	}
}

func TestEnrichJiraAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	ctx := context.Background()
	markdown, enrichData, err := jp.enrichJiraIssueContext(ctx, "test-token", "TEST-123")

	if err == nil {
		t.Error("Expected error from API failure")
	}

	// Should still return some context
	if !strings.Contains(markdown, "Could not fetch issue TEST-123") {
		t.Error("Missing error message in output")
	}

	if enrichData != nil {
		t.Error("Expected nil enrichData on error")
	}
}

func TestEnrichJiraDescriptionTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		longDescription := strings.Repeat("This is a very long description. ", 200) // > maxBodyLen
		response := `{
			"key": "TEST-123",
			"fields": {
				"summary": "Test Issue",
				"description": "` + longDescription + `",
				"status": {"name": "Open"},
				"issuetype": {"name": "Bug"},
				"priority": {"name": "Low"},
				"components": [],
				"labels": [],
				"issuelinks": [],
				"attachment": []
			}
		}`

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(response))
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	ctx := context.Background()
	markdown, _, err := jp.enrichJiraIssueContext(ctx, "test-token", "TEST-123")

	if err != nil {
		t.Fatalf("enrichJiraIssueContext failed: %v", err)
	}

	if !strings.Contains(markdown, "... (truncated)") {
		t.Error("Long description should be truncated")
	}

	// Ensure the description doesn't exceed maxBodyLen in the markdown
	lines := strings.Split(markdown, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "This is a very long description") {
			if len(line) > maxBodyLen+20 { // Some buffer for markdown
				t.Error("Description line is too long even after truncation")
			}
		}
	}
}

func TestBuildJiraTriggerContext(t *testing.T) {
	issue := &jiraIssue{
		Key: "TEST-123",
		Fields: struct {
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee *struct {
				DisplayName string `json:"displayName"`
			} `json:"assignee"`
			Reporter *struct {
				DisplayName string `json:"displayName"`
			} `json:"reporter"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			Labels     []string `json:"labels"`
			IssueLinks []struct {
				Type struct {
					Name string `json:"name"`
				} `json:"type"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			Summary:     "Test Issue",
			Description: "Test description",
			Status:      struct{ Name string }{Name: "Open"},
			IssueType:   struct{ Name string }{Name: "Bug"},
			Priority:    struct{ Name string }{Name: "High"},
			Assignee:    &struct{ DisplayName string }{DisplayName: "John Doe"},
			Reporter:    &struct{ DisplayName string }{DisplayName: "Jane Smith"},
			Labels:      []string{"urgent", "bug"},
			Attachment: []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			}{
				{Filename: "test.log", Size: 1024, MimeType: "text/plain"},
			},
		},
	}

	enrichData := &enrichmentData{
		issue: issue,
		comments: []jiraComment{
			{
				Body: "Test comment",
				Author: struct {
					DisplayName string `json:"displayName"`
				}{DisplayName: "Bob Wilson"},
				Created: "2023-05-01T10:00:00Z",
			},
		},
		sprint: &jiraSprint{
			Name:  "Sprint 1",
			State: "ACTIVE",
		},
	}

	jp := &JiraPoller{baseURL: "https://test.atlassian.net"}
	context := jp.buildJiraTriggerContext(enrichData, "enriched markdown content")

	expectedKeys := []string{
		"issue_key", "issue_title", "issue_body", "issue_url", "issue_status",
		"issue_labels", "issue_type", "enriched_context", "issue_assignee",
		"issue_reporter", "issue_priority", "issue_comments", "issue_sprint",
		"issue_linked_issues", "issue_attachments",
	}

	for _, key := range expectedKeys {
		if _, exists := context[key]; !exists {
			t.Errorf("Missing expected key: %s", key)
		}
	}

	// Check specific values
	if context["issue_key"] != "TEST-123" {
		t.Errorf("Expected issue_key TEST-123, got %v", context["issue_key"])
	}
	if context["issue_assignee"] != "John Doe" {
		t.Errorf("Expected issue_assignee John Doe, got %v", context["issue_assignee"])
	}
	if context["issue_priority"] != "High" {
		t.Errorf("Expected issue_priority High, got %v", context["issue_priority"])
	}
	if context["issue_sprint"] != "Sprint 1 (ACTIVE)" {
		t.Errorf("Expected sprint info, got %v", context["issue_sprint"])
	}
	if context["enriched_context"] != "enriched markdown content" {
		t.Errorf("Expected enriched_context to match, got %v", context["enriched_context"])
	}

	commentsText, ok := context["issue_comments"].(string)
	if !ok {
		t.Error("issue_comments should be a string")
	} else if !strings.Contains(commentsText, "Bob Wilson: Test comment") {
		t.Errorf("Expected comment text, got %s", commentsText)
	}
}