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
	"time"
)

func TestEnrichJiraIssueContext(t *testing.T) {
	// Mock JIRA server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"key": "TEST-123",
				"fields": {
					"summary": "Test Issue Summary",
					"description": "This is a test issue description with some content.",
					"status": {"name": "In Progress"},
					"issuetype": {"name": "Task"},
					"priority": {"name": "High"},
					"assignee": {
						"displayName": "John Doe",
						"emailAddress": "john.doe@example.com"
					},
					"reporter": {
						"displayName": "Jane Smith",
						"emailAddress": "jane.smith@example.com"
					},
					"components": [{"name": "Frontend"}, {"name": "Backend"}],
					"labels": ["urgent", "bug-fix"],
					"issuelinks": [{
						"type": {
							"name": "Blocks",
							"inward": "is blocked by",
							"outward": "blocks"
						},
						"outwardIssue": {
							"key": "TEST-456",
							"fields": {"summary": "Related Issue"}
						}
					}],
					"attachment": [{
						"filename": "screenshot.png",
						"size": 1024000,
						"mimeType": "image/png"
					}]
				}
			}`))
		case strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123/comment"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"comments": [{
					"body": "This is the first comment.",
					"author": {"displayName": "Bob Wilson"},
					"created": "2026-05-05T10:30:00.000Z"
				}]
			}`))
		case strings.Contains(r.URL.Path, "/rest/agile/1.0/issue/TEST-123"):
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"sprint": [{
					"name": "Sprint 1",
					"state": "active"
				}]
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

	markdown, additionalContext := jp.enrichJiraIssueContext(context.Background(), "fake-token", "TEST-123")

	// Verify markdown contains expected sections
	expectedSections := []string{
		"## Event Context",
		"### Issue TEST-123: Test Issue Summary",
		"**Status**: In Progress",
		"**Type**: Task",
		"**Priority**: High",
		"**Assignee**: John Doe",
		"**Reporter**: Jane Smith",
		"**Components**: Frontend, Backend",
		"**Labels**: urgent, bug-fix",
		"**Description**:",
		"This is a test issue description",
		"### Comments (1)",
		"**Bob Wilson**",
		"This is the first comment",
		"### Sprint Context",
		"**Sprint 1** (active)",
		"### Linked Issues",
		"[blocks] TEST-456: Related Issue",
		"### Attachments",
		"screenshot.png (1.0 MB, image/png)",
	}

	for _, section := range expectedSections {
		if !strings.Contains(markdown, section) {
			t.Errorf("Expected markdown to contain '%s', got:\n%s", section, markdown)
		}
	}

	// Verify additional context fields
	expectedFields := map[string]string{
		"issue_priority":      "High",
		"issue_assignee":      "John Doe",
		"issue_reporter":      "Jane Smith",
		"issue_components":    "Frontend, Backend",
		"issue_comments":      "Bob Wilson (2026-05-05): This is the first comment.",
		"issue_sprint":        "Sprint 1 (active)",
		"issue_linked_issues": "[blocks] TEST-456: Related Issue",
		"issue_attachments":   "screenshot.png",
	}

	for field, expectedValue := range expectedFields {
		if value, ok := additionalContext[field]; !ok {
			t.Errorf("Expected additional context to contain field '%s'", field)
		} else if value != expectedValue {
			t.Errorf("Expected %s = '%s', got '%s'", field, expectedValue, value)
		}
	}
}

func TestEnrichJiraCommentsTruncation(t *testing.T) {
	longComment := strings.Repeat("This is a very long comment. ", 100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/comment") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"comments": [{
					"body": "` + longComment + `",
					"author": {"displayName": "Test User"},
					"created": "2026-05-05T10:30:00.000Z"
				}]
			}`))
		}
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	var sb strings.Builder
	commentsText := jp.enrichJiraComments(context.Background(), "fake-token", "TEST-123", &sb)

	// Verify comment was truncated
	if len(commentsText) <= maxCommentLen {
		t.Errorf("Expected comment to be truncated, but it wasn't. Length: %d", len(commentsText))
	}

	if !strings.Contains(commentsText, "... (truncated)") {
		t.Errorf("Expected truncated comment to contain '... (truncated)', got: %s", commentsText)
	}
}

func TestEnrichJiraAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "Issue not found"}`))
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	markdown, additionalContext := jp.enrichJiraIssueContext(context.Background(), "fake-token", "NONEXISTENT-123")

	// Verify graceful degradation
	if !strings.Contains(markdown, "Could not fetch issue") {
		t.Errorf("Expected markdown to indicate fetch error, got: %s", markdown)
	}

	// Additional context should be empty on error
	if len(additionalContext) != 0 {
		t.Errorf("Expected empty additional context on error, got: %v", additionalContext)
	}
}

func TestEnrichJiraDescriptionTruncation(t *testing.T) {
	longDescription := strings.Repeat("This is a very long description. ", 200)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/rest/api/2/issue/TEST-123") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"key": "TEST-123",
				"fields": {
					"summary": "Test Issue",
					"description": "` + longDescription + `",
					"status": {"name": "Open"},
					"issuetype": {"name": "Bug"},
					"priority": {"name": "Medium"},
					"assignee": null,
					"reporter": null,
					"components": [],
					"labels": [],
					"issuelinks": [],
					"attachment": []
				}
			}`))
		}
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	markdown, _ := jp.enrichJiraIssueContext(context.Background(), "fake-token", "TEST-123")

	// Verify description was truncated
	if !strings.Contains(markdown, "... (truncated)") {
		t.Errorf("Expected description to be truncated with '... (truncated)', got: %s", markdown)
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		bytes    int
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, test := range tests {
		result := formatFileSize(test.bytes)
		if result != test.expected {
			t.Errorf("formatFileSize(%d) = %s, expected %s", test.bytes, result, test.expected)
		}
	}
}

func TestEnrichJiraSprintContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/rest/agile/1.0/issue/TEST-123") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"sprint": [
					{"name": "Sprint 1", "state": "active"},
					{"name": "Sprint 2", "state": "future"}
				]
			}`))
		}
	}))
	defer server.Close()

	jp := &JiraPoller{
		baseURL: server.URL,
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	var sb strings.Builder
	sprintText := jp.enrichJiraSprintContext(context.Background(), "fake-token", "TEST-123", &sb)

	expected := "Sprint 1 (active), Sprint 2 (future)"
	if sprintText != expected {
		t.Errorf("Expected sprint text '%s', got '%s'", expected, sprintText)
	}

	markdown := sb.String()
	if !strings.Contains(markdown, "### Sprint Context") {
		t.Errorf("Expected markdown to contain sprint section, got: %s", markdown)
	}
}

func TestEnrichJiraLinkedIssues(t *testing.T) {
	issue := &JiraIssue{
		Key: "TEST-123",
		Fields: struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"assignee"`
			Reporter struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"reporter"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			Labels     []string `json:"labels"`
			IssueLinks []struct {
				Type struct {
					Name    string `json:"name"`
					Inward  string `json:"inward"`
					Outward string `json:"outward"`
				} `json:"type"`
				InwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int    `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			IssueLinks: []struct {
				Type struct {
					Name    string `json:"name"`
					Inward  string `json:"inward"`
					Outward string `json:"outward"`
				} `json:"type"`
				InwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			}{
				{
					Type: struct {
						Name    string `json:"name"`
						Inward  string `json:"inward"`
						Outward string `json:"outward"`
					}{Name: "Blocks", Inward: "is blocked by", Outward: "blocks"},
					OutwardIssue: struct {
						Key    string `json:"key"`
						Fields struct {
							Summary string `json:"summary"`
						} `json:"fields"`
					}{Key: "TEST-456", Fields: struct {
						Summary string `json:"summary"`
					}{Summary: "Blocked Issue"}},
				},
			},
		},
	}

	jp := &JiraPoller{}
	var sb strings.Builder
	linkedText := jp.enrichJiraLinkedIssues(issue, &sb)

	expected := "[blocks] TEST-456: Blocked Issue"
	if linkedText != expected {
		t.Errorf("Expected linked issues text '%s', got '%s'", expected, linkedText)
	}

	markdown := sb.String()
	if !strings.Contains(markdown, "### Linked Issues") {
		t.Errorf("Expected markdown to contain linked issues section, got: %s", markdown)
	}
}

func TestEnrichJiraAttachments(t *testing.T) {
	issue := &JiraIssue{
		Key: "TEST-123",
		Fields: struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			IssueType struct {
				Name string `json:"name"`
			} `json:"issuetype"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"assignee"`
			Reporter struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"reporter"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			Labels     []string `json:"labels"`
			IssueLinks []struct {
				Type struct {
					Name    string `json:"name"`
					Inward  string `json:"inward"`
					Outward string `json:"outward"`
				} `json:"type"`
				InwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
				OutwardIssue struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int    `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			Attachment: []struct {
				Filename string `json:"filename"`
				Size     int    `json:"size"`
				MimeType string `json:"mimeType"`
			}{
				{Filename: "screenshot.png", Size: 1024000, MimeType: "image/png"},
				{Filename: "logs.txt", Size: 2048, MimeType: "text/plain"},
			},
		},
	}

	jp := &JiraPoller{}
	var sb strings.Builder
	attachmentsText := jp.enrichJiraAttachments(issue, &sb)

	expected := "screenshot.png, logs.txt"
	if attachmentsText != expected {
		t.Errorf("Expected attachments text '%s', got '%s'", expected, attachmentsText)
	}

	markdown := sb.String()
	if !strings.Contains(markdown, "### Attachments") {
		t.Errorf("Expected markdown to contain attachments section, got: %s", markdown)
	}
	if !strings.Contains(markdown, "screenshot.png (1.0 MB, image/png)") {
		t.Errorf("Expected markdown to contain attachment details, got: %s", markdown)
	}
}
