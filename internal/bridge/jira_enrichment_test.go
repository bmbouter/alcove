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
	"time"
)

func TestEnrichJiraIssueContext(t *testing.T) {
	mux := http.NewServeMux()

	// Mock full issue endpoint
	mux.HandleFunc("/rest/api/2/issue/TEST-123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "TEST-123",
			"fields": map[string]interface{}{
				"summary":     "Test Issue Summary",
				"description": "This is a test issue description with details.",
				"status":      map[string]string{"name": "In Progress"},
				"issuetype":   map[string]string{"name": "Bug"},
				"priority":    map[string]string{"name": "High"},
				"assignee": map[string]string{
					"displayName":    "John Doe",
					"emailAddress": "john.doe@example.com",
				},
				"reporter": map[string]string{
					"displayName":    "Jane Smith",
					"emailAddress": "jane.smith@example.com",
				},
				"labels":     []string{"urgent", "customer-issue"},
				"components": []map[string]string{{"name": "UI"}, {"name": "Backend"}},
				"issuelinks": []map[string]interface{}{
					{
						"type": map[string]interface{}{
							"name":    "Blocks",
							"outward": "blocks",
							"inward":  "is blocked by",
						},
						"outwardIssue": map[string]interface{}{
							"key":    "TEST-124",
							"fields": map[string]string{"summary": "Related Issue"},
						},
					},
				},
				"attachment": []map[string]interface{}{
					{
						"filename": "screenshot.png",
						"size":     1024000,
						"mimeType": "image/png",
					},
				},
			},
		})
	})

	// Mock comments endpoint
	mux.HandleFunc("/rest/api/2/issue/TEST-123/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"comments": []map[string]interface{}{
				{
					"body":    "First comment with important details.",
					"created": "2026-01-15T10:00:00.000Z",
					"author": map[string]string{
						"displayName":    "Bob Wilson",
						"emailAddress": "bob.wilson@example.com",
					},
				},
				{
					"body":    "Second comment providing updates.",
					"created": "2026-01-16T12:30:00.000Z",
					"author": map[string]string{
						"displayName":    "Alice Brown",
						"emailAddress": "alice.brown@example.com",
					},
				},
			},
		})
	})

	// Mock sprint endpoint
	mux.HandleFunc("/rest/agile/1.0/issue/TEST-123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"fields": map[string]interface{}{
				"sprint": map[string]interface{}{
					"name":  "Sprint 24",
					"state": "ACTIVE",
				},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "TEST-123")

	// Check that the enriched context contains expected data
	checks := []string{
		"## Event Context",
		"**Event**: jira / issue_updated",
		"**Issue Key**: TEST-123",
		"### Issue: Test Issue Summary",
		"**Status**: In Progress",
		"**Type**: Bug",
		"**Priority**: High",
		"**Assignee**: John Doe",
		"**Reporter**: Jane Smith",
		"**Labels**: urgent, customer-issue",
		"**Components**: UI, Backend",
		"This is a test issue description with details.",
		"### Linked Issues",
		"[blocks] TEST-124: Related Issue",
		"### Attachments",
		"screenshot.png (1000.0 KB, image/png)",
		"### Comments (2)",
		"**Bob Wilson** (2026-01-15):",
		"First comment with important details.",
		"**Alice Brown** (2026-01-16):",
		"Second comment providing updates.",
		"### Sprint",
		"**Name**: Sprint 24",
		"**State**: ACTIVE",
		"---",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("enriched context missing expected content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichJiraCommentTruncation(t *testing.T) {
	mux := http.NewServeMux()

	// Mock issue endpoint
	mux.HandleFunc("/rest/api/2/issue/TEST-456", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "TEST-456",
			"fields": map[string]interface{}{
				"summary":     "Test Issue",
				"description": "Short description",
				"status":      map[string]string{"name": "Open"},
				"issuetype":   map[string]string{"name": "Task"},
				"priority":    map[string]string{"name": "Medium"},
				"assignee":    nil,
				"reporter": map[string]string{
					"displayName": "Test User",
				},
				"labels":      []string{},
				"components":  []interface{}{},
				"issuelinks":  []interface{}{},
				"attachment":  []interface{}{},
			},
		})
	})

	longComment := strings.Repeat("x", maxCommentLen+500)
	mux.HandleFunc("/rest/api/2/issue/TEST-456/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"comments": []map[string]interface{}{
				{
					"body":    longComment,
					"created": "2026-01-20T10:00:00.000Z",
					"author": map[string]string{
						"displayName": "Long Commenter",
					},
				},
			},
		})
	})

	mux.HandleFunc("/rest/agile/1.0/issue/TEST-456", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"fields": map[string]interface{}{},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "TEST-456")

	// Comment should be truncated
	if !strings.Contains(result, "... (truncated)") {
		t.Error("expected truncation marker in output")
	}

	// Full long comment should NOT appear
	if strings.Contains(result, longComment) {
		t.Error("full long comment should not appear in output")
	}
}

func TestEnrichJiraLinkedIssues(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/api/2/issue/TEST-789", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "TEST-789",
			"fields": map[string]interface{}{
				"summary":     "Main Issue",
				"description": "Main issue description",
				"status":      map[string]string{"name": "Open"},
				"issuetype":   map[string]string{"name": "Story"},
				"priority":    map[string]string{"name": "Low"},
				"assignee":    nil,
				"reporter":    map[string]string{"displayName": "Test User"},
				"labels":      []string{},
				"components":  []interface{}{},
				"attachment":  []interface{}{},
				"issuelinks": []map[string]interface{}{
					{
						"type": map[string]interface{}{
							"name":    "Relates",
							"outward": "relates to",
							"inward":  "relates to",
						},
						"outwardIssue": map[string]interface{}{
							"key":    "TEST-790",
							"fields": map[string]string{"summary": "Outward Related Issue"},
						},
					},
					{
						"type": map[string]interface{}{
							"name":    "Duplicates",
							"outward": "duplicates",
							"inward":  "is duplicated by",
						},
						"inwardIssue": map[string]interface{}{
							"key":    "TEST-791",
							"fields": map[string]string{"summary": "Inward Duplicate Issue"},
						},
					},
				},
			},
		})
	})

	mux.HandleFunc("/rest/api/2/issue/TEST-789/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"comments": []interface{}{}})
	})

	mux.HandleFunc("/rest/agile/1.0/issue/TEST-789", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"fields": map[string]interface{}{}})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "TEST-789")

	checks := []string{
		"### Linked Issues",
		"[relates to] TEST-790: Outward Related Issue",
		"[is duplicated by] TEST-791: Inward Duplicate Issue",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("missing linked issue content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichJiraSprintContext(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/api/2/issue/SPRINT-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "SPRINT-1",
			"fields": map[string]interface{}{
				"summary":     "Sprint Issue",
				"description": "Issue in sprint",
				"status":      map[string]string{"name": "In Progress"},
				"issuetype":   map[string]string{"name": "Epic"},
				"priority":    map[string]string{"name": "High"},
				"assignee":    nil,
				"reporter":    map[string]string{"displayName": "Sprint Master"},
				"labels":      []string{},
				"components":  []interface{}{},
				"issuelinks":  []interface{}{},
				"attachment":  []interface{}{},
			},
		})
	})

	mux.HandleFunc("/rest/api/2/issue/SPRINT-1/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"comments": []interface{}{}})
	})

	mux.HandleFunc("/rest/agile/1.0/issue/SPRINT-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"fields": map[string]interface{}{
				"sprint": map[string]interface{}{
					"name":  "Release 2.0 Sprint 1",
					"state": "CLOSED",
				},
			},
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "SPRINT-1")

	checks := []string{
		"### Sprint",
		"**Name**: Release 2.0 Sprint 1",
		"**State**: CLOSED",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("missing sprint content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichJiraAttachmentMetadata(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/api/2/issue/ATTACH-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "ATTACH-1",
			"fields": map[string]interface{}{
				"summary":     "Issue with Attachments",
				"description": "Has multiple files",
				"status":      map[string]string{"name": "Open"},
				"issuetype":   map[string]string{"name": "Bug"},
				"priority":    map[string]string{"name": "Medium"},
				"assignee":    nil,
				"reporter":    map[string]string{"displayName": "File Uploader"},
				"labels":      []string{},
				"components":  []interface{}{},
				"issuelinks":  []interface{}{},
				"attachment": []map[string]interface{}{
					{
						"filename": "error_log.txt",
						"size":     2048,
						"mimeType": "text/plain",
					},
					{
						"filename": "screenshot.png",
						"size":     1572864, // 1.5 MB
						"mimeType": "image/png",
					},
					{
						"filename": "large_file.pdf",
						"size":     10485760, // 10 MB
						"mimeType": "application/pdf",
					},
				},
			},
		})
	})

	mux.HandleFunc("/rest/api/2/issue/ATTACH-1/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"comments": []interface{}{}})
	})

	mux.HandleFunc("/rest/agile/1.0/issue/ATTACH-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"fields": map[string]interface{}{}})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "ATTACH-1")

	checks := []string{
		"### Attachments",
		"error_log.txt (2.0 KB, text/plain)",
		"screenshot.png (1.5 MB, image/png)",
		"large_file.pdf (10.0 MB, application/pdf)",
	}

	for _, check := range checks {
		if !strings.Contains(result, check) {
			t.Errorf("missing attachment content: %q\n\nFull result:\n%s", check, result)
		}
	}
}

func TestEnrichJiraAPIError(t *testing.T) {
	mux := http.NewServeMux()

	// Return 404 for issue
	mux.HandleFunc("/rest/api/2/issue/MISSING-123", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errorMessages":["Issue does not exist or you do not have permission to see it."],"errors":{}}`)
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "MISSING-123")

	// Should not panic and should contain error info
	if !strings.Contains(result, "Could not fetch full issue details") {
		t.Errorf("expected error message for 404, got:\n%s", result)
	}

	// Should still have the event context header
	if !strings.Contains(result, "## Event Context") {
		t.Error("missing event context header")
	}
}

func TestEnrichJiraDescriptionTruncation(t *testing.T) {
	mux := http.NewServeMux()

	longDescription := strings.Repeat("y", maxBodyLen+500)

	mux.HandleFunc("/rest/api/2/issue/LONG-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"key": "LONG-1",
			"fields": map[string]interface{}{
				"summary":     "Issue with Long Description",
				"description": longDescription,
				"status":      map[string]string{"name": "Open"},
				"issuetype":   map[string]string{"name": "Story"},
				"priority":    map[string]string{"name": "Low"},
				"assignee":    nil,
				"reporter":    map[string]string{"displayName": "Verbose Writer"},
				"labels":      []string{},
				"components":  []interface{}{},
				"issuelinks":  []interface{}{},
				"attachment":  []interface{}{},
			},
		})
	})

	mux.HandleFunc("/rest/api/2/issue/LONG-1/comment", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"comments": []interface{}{}})
	})

	mux.HandleFunc("/rest/agile/1.0/issue/LONG-1", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"fields": map[string]interface{}{}})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	poller := &JiraPoller{
		client:  ts.Client(),
		baseURL: ts.URL,
	}

	result := poller.enrichJiraIssueContext(context.Background(), "fake-token", "LONG-1")

	// Description should be truncated
	if !strings.Contains(result, "... (truncated)") {
		t.Error("expected truncation marker in output")
	}

	// Full long description should NOT appear
	if strings.Contains(result, longDescription) {
		t.Error("full long description should not appear in output")
	}
}

func TestBuildJiraTriggerContext(t *testing.T) {
	issue := &jiraFullIssue{
		Key: "BUILD-1",
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
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"assignee"`
			Reporter struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"reporter"`
			Labels     []string `json:"labels"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
			IssueLinks []struct {
				Type struct {
					Name    string `json:"name"`
					Inward  string `json:"inward"`
					Outward string `json:"outward"`
				} `json:"type"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
			} `json:"issuelinks"`
			Attachment []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		}{
			Summary:     "Build Test Issue",
			Description: "Test description for trigger context",
			Status:      struct{ Name string `json:"name"` }{"Done"},
			IssueType:   struct{ Name string `json:"name"` }{"Task"},
			Priority:    struct{ Name string `json:"name"` }{"High"},
			Assignee: &struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			}{
				DisplayName:  "Test Assignee",
				EmailAddress: "test@example.com",
			},
			Reporter: struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			}{
				DisplayName:  "Test Reporter",
				EmailAddress: "reporter@example.com",
			},
			Labels: []string{"test", "automation"},
			Components: []struct{ Name string `json:"name"` }{
				{"Component1"}, {"Component2"},
			},
			IssueLinks: []struct {
				Type struct {
					Name    string `json:"name"`
					Inward  string `json:"inward"`
					Outward string `json:"outward"`
				} `json:"type"`
				OutwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"outwardIssue"`
				InwardIssue *struct {
					Key    string `json:"key"`
					Fields struct {
						Summary string `json:"summary"`
					} `json:"fields"`
				} `json:"inwardIssue"`
			}{
				{
					Type: struct {
						Name    string `json:"name"`
						Inward  string `json:"inward"`
						Outward string `json:"outward"`
					}{"Blocks", "is blocked by", "blocks"},
					OutwardIssue: &struct {
						Key    string `json:"key"`
						Fields struct {
							Summary string `json:"summary"`
						} `json:"fields"`
					}{"BUILD-2", struct{ Summary string `json:"summary"` }{"Related Issue"}},
				},
			},
			Attachment: []struct {
				Filename string `json:"filename"`
				Size     int64  `json:"size"`
				MimeType string `json:"mimeType"`
			}{
				{"test.txt", 1024, "text/plain"},
			},
		},
	}

	comments := &jiraComments{
		Comments: []struct {
			Body   string `json:"body"`
			Author struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			} `json:"author"`
			Created string `json:"created"`
		}{
			{
				Body:    "Test comment body",
				Author:  struct{ DisplayName, EmailAddress string `json:"displayName,emailAddress"` }{"Commenter", "commenter@example.com"},
				Created: "2026-01-15T10:00:00.000Z",
			},
		},
	}

	sprintInfo := &jiraAgileIssue{
		Fields: struct {
			Sprint *struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"sprint"`
		}{
			Sprint: &struct {
				Name  string `json:"name"`
				State string `json:"state"`
			}{"Test Sprint", "ACTIVE"},
		},
	}

	poller := &JiraPoller{baseURL: "https://test.atlassian.net"}
	result := poller.buildJiraTriggerContextWithData(issue, "## Enriched markdown content", comments, sprintInfo)

	// Check all expected keys are present
	expectedKeys := []string{
		"issue_key", "issue_title", "issue_body", "issue_url", "issue_status",
		"issue_labels", "issue_type", "enriched_context", "issue_priority",
		"issue_reporter", "issue_assignee", "issue_linked_issues",
		"issue_attachments", "issue_comments", "issue_sprint",
	}

	for _, key := range expectedKeys {
		if _, exists := result[key]; !exists {
			t.Errorf("missing expected key in trigger context: %s", key)
		}
	}

	// Check specific values
	if result["issue_key"] != "BUILD-1" {
		t.Errorf("expected issue_key BUILD-1, got %v", result["issue_key"])
	}

	if result["issue_assignee"] != "Test Assignee" {
		t.Errorf("expected assignee Test Assignee, got %v", result["issue_assignee"])
	}

	if result["enriched_context"] != "## Enriched markdown content" {
		t.Errorf("expected enriched context to be preserved, got %v", result["enriched_context"])
	}

	if !strings.Contains(result["issue_linked_issues"].(string), "[blocks] BUILD-2: Related Issue") {
		t.Errorf("expected linked issues to be formatted correctly, got %v", result["issue_linked_issues"])
	}

	if !strings.Contains(result["issue_comments"].(string), "Commenter (2026-01-15): Test comment body") {
		t.Errorf("expected comments to be formatted correctly, got %v", result["issue_comments"])
	}

	if result["issue_sprint"] != "Test Sprint (ACTIVE)" {
		t.Errorf("expected sprint to be formatted correctly, got %v", result["issue_sprint"])
	}

	if result["issue_attachments"] != "test.txt" {
		t.Errorf("expected attachments to be formatted correctly, got %v", result["issue_attachments"])
	}
}
