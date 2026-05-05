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
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// GitLabEnricher provides GitLab event enrichment capabilities.
type GitLabEnricher struct {
	client *http.Client
}

// NewGitLabEnricher creates a new GitLab enricher with the given HTTP client.
func NewGitLabEnricher(client *http.Client) *GitLabEnricher {
	if client == nil {
		client = &http.Client{}
	}
	return &GitLabEnricher{client: client}
}

// EnrichGitLabEventContext fetches rich context from GitLab for the given event
// and returns a structured preamble string that provides the LLM with
// full MR/issue/pipeline details without needing to make its own API calls.
func (e *GitLabEnricher) EnrichGitLabEventContext(ctx context.Context, token, apiHost, eventType, action string, meta map[string]string) string {
	project := meta["GITLAB_PROJECT"]
	mrIID := meta["GITLAB_MR_IID"]
	issueIID := meta["GITLAB_ISSUE_IID"]

	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString(fmt.Sprintf("**Event**: %s / %s\n", eventType, action))
	sb.WriteString(fmt.Sprintf("**Project**: %s\n", project))

	if label := meta["GITLAB_LABEL_ADDED"]; label != "" {
		sb.WriteString(fmt.Sprintf("**Label Added**: %s\n", label))
	}
	sb.WriteString("\n")

	switch {
	case mrIID != "" && (eventType == "merge_request" || eventType == "note"):
		e.enrichMRContext(ctx, token, apiHost, project, mrIID, &sb)
	case issueIID != "" && (eventType == "issue" || eventType == "note"):
		e.enrichGitLabIssueContext(ctx, token, apiHost, project, issueIID, &sb)
	default:
		sb.WriteString("No additional context available for this event type.\n")
	}

	sb.WriteString("\n---\n")
	return sb.String()
}

// enrichMRContext fetches and formats GitLab merge request details, notes, and pipeline status.
func (e *GitLabEnricher) enrichMRContext(ctx context.Context, token, apiHost, project, mrIID string, sb *strings.Builder) {
	// URL-encode the project path
	encodedProject := url.PathEscape(project)

	// Fetch MR details
	mrURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s", apiHost, encodedProject, mrIID)
	data, err := e.gitlabAPIGet(ctx, token, mrURL)
	if err != nil {
		log.Printf("enrichment: could not fetch MR !%s: %v", mrIID, err)
		sb.WriteString(fmt.Sprintf("Could not fetch MR !%s: %v\n", mrIID, err))
		return
	}

	var mr struct {
		Title        string   `json:"title"`
		Description  string   `json:"description"`
		State        string   `json:"state"`
		SourceBranch string   `json:"source_branch"`
		TargetBranch string   `json:"target_branch"`
		Author       struct {
			Username string `json:"username"`
		} `json:"author"`
		Labels   []string `json:"labels"`
		WebURL   string   `json:"web_url"`
		SHA      string   `json:"sha"`
	}
	if err := json.Unmarshal(data, &mr); err != nil {
		log.Printf("enrichment: error parsing MR !%s: %v", mrIID, err)
		return
	}

	sb.WriteString(fmt.Sprintf("### MR !%s: %s\n\n", mrIID, mr.Title))
	sb.WriteString(fmt.Sprintf("**State**: %s\n", mr.State))
	sb.WriteString(fmt.Sprintf("**Author**: @%s\n", mr.Author.Username))
	sb.WriteString(fmt.Sprintf("**Branch**: %s → %s\n", mr.SourceBranch, mr.TargetBranch))
	if mr.SHA != "" {
		sb.WriteString(fmt.Sprintf("**SHA**: %s\n", mr.SHA))
	}

	if len(mr.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(mr.Labels, ", ")))
	}

	sb.WriteString("\n**Description**:\n")
	description := mr.Description
	if len(description) > maxBodyLen {
		description = description[:maxBodyLen] + "\n... (truncated)"
	}
	if description == "" {
		description = "(empty)"
	}
	sb.WriteString(description + "\n")

	// Fetch notes (comments)
	notesURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/notes?per_page=%d", apiHost, encodedProject, mrIID, maxCommentsNum)
	notesData, err := e.gitlabAPIGet(ctx, token, notesURL)
	if err != nil {
		log.Printf("enrichment: could not fetch notes for MR !%s: %v", mrIID, err)
	} else {
		var notes []struct {
			Body   string `json:"body"`
			Author struct {
				Username string `json:"username"`
			} `json:"author"`
			CreatedAt string `json:"created_at"`
			System    bool   `json:"system"`
		}
		if err := json.Unmarshal(notesData, &notes); err == nil && len(notes) > 0 {
			var userNotes []struct {
				Body      string
				Author    string
				CreatedAt string
			}
			for _, n := range notes {
				if !n.System { // Skip system notes
					userNotes = append(userNotes, struct {
						Body      string
						Author    string
						CreatedAt string
					}{n.Body, n.Author.Username, n.CreatedAt})
				}
			}

			if len(userNotes) > 0 {
				sb.WriteString(fmt.Sprintf("\n### Notes (%d)\n\n", len(userNotes)))
				for _, n := range userNotes {
					note := n.Body
					if len(note) > maxCommentLen {
						note = note[:maxCommentLen] + "\n... (truncated)"
					}
					dateStr := n.CreatedAt
					if len(dateStr) >= 10 {
						dateStr = dateStr[:10]
					}
					sb.WriteString(fmt.Sprintf("**@%s** (%s):\n%s\n\n", n.Author, dateStr, note))
				}
			}
		}
	}

	// Fetch pipeline status
	pipelinesURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/pipelines", apiHost, encodedProject, mrIID)
	pipelineData, err := e.gitlabAPIGet(ctx, token, pipelinesURL)
	if err == nil {
		var pipelines []struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		}
		if err := json.Unmarshal(pipelineData, &pipelines); err == nil && len(pipelines) > 0 {
			sb.WriteString("\n### Pipeline Status\n\n")
			latest := pipelines[0] // GitLab returns most recent first
			sb.WriteString(fmt.Sprintf("- Pipeline #%d (%s)\n", latest.ID, latest.Status))
		}
	}
}

// enrichGitLabIssueContext fetches and formats GitLab issue details and notes.
func (e *GitLabEnricher) enrichGitLabIssueContext(ctx context.Context, token, apiHost, project, issueIID string, sb *strings.Builder) {
	// URL-encode the project path
	encodedProject := url.PathEscape(project)

	// Fetch issue details
	issueURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%s", apiHost, encodedProject, issueIID)
	data, err := e.gitlabAPIGet(ctx, token, issueURL)
	if err != nil {
		log.Printf("enrichment: could not fetch issue #%s: %v", issueIID, err)
		sb.WriteString(fmt.Sprintf("Could not fetch issue #%s: %v\n", issueIID, err))
		return
	}

	var issue struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		State       string   `json:"state"`
		Author      struct {
			Username string `json:"username"`
		} `json:"author"`
		Labels    []string `json:"labels"`
		Assignees []struct {
			Username string `json:"username"`
		} `json:"assignees"`
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		log.Printf("enrichment: error parsing issue #%s: %v", issueIID, err)
		return
	}

	sb.WriteString(fmt.Sprintf("### Issue #%s: %s\n\n", issueIID, issue.Title))
	sb.WriteString(fmt.Sprintf("**State**: %s\n", issue.State))
	sb.WriteString(fmt.Sprintf("**Author**: @%s\n", issue.Author.Username))

	if len(issue.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(issue.Labels, ", ")))
	}
	if len(issue.Assignees) > 0 {
		var assigneeNames []string
		for _, a := range issue.Assignees {
			assigneeNames = append(assigneeNames, "@"+a.Username)
		}
		sb.WriteString(fmt.Sprintf("**Assignees**: %s\n", strings.Join(assigneeNames, ", ")))
	}

	sb.WriteString("\n**Description**:\n")
	description := issue.Description
	if len(description) > maxBodyLen {
		description = description[:maxBodyLen] + "\n... (truncated)"
	}
	if description == "" {
		description = "(empty)"
	}
	sb.WriteString(description + "\n")

	// Fetch notes (comments)
	notesURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%s/notes?per_page=%d", apiHost, encodedProject, issueIID, maxCommentsNum)
	notesData, err := e.gitlabAPIGet(ctx, token, notesURL)
	if err != nil {
		log.Printf("enrichment: could not fetch notes for issue #%s: %v", issueIID, err)
		return
	}

	var notes []struct {
		Body   string `json:"body"`
		Author struct {
			Username string `json:"username"`
		} `json:"author"`
		CreatedAt string `json:"created_at"`
		System    bool   `json:"system"`
	}
	if err := json.Unmarshal(notesData, &notes); err != nil {
		log.Printf("enrichment: error parsing notes for issue #%s: %v", issueIID, err)
		return
	}

	var userNotes []struct {
		Body      string
		Author    string
		CreatedAt string
	}
	for _, n := range notes {
		if !n.System { // Skip system notes
			userNotes = append(userNotes, struct {
				Body      string
				Author    string
				CreatedAt string
			}{n.Body, n.Author.Username, n.CreatedAt})
		}
	}

	if len(userNotes) > 0 {
		sb.WriteString(fmt.Sprintf("\n### Notes (%d)\n\n", len(userNotes)))
		for _, n := range userNotes {
			note := n.Body
			if len(note) > maxCommentLen {
				note = note[:maxCommentLen] + "\n... (truncated)"
			}
			dateStr := n.CreatedAt
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}
			sb.WriteString(fmt.Sprintf("**@%s** (%s):\n%s\n\n", n.Author, dateStr, note))
		}
	}
}

// gitlabAPIGet performs an authenticated GET to the GitLab API.
func (e *GitLabEnricher) gitlabAPIGet(ctx context.Context, token, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("User-Agent", "alcove-enricher")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitLab API returned %d", resp.StatusCode)
	}
	return respBody, nil
}

// ExtractGitLabIssueContext extracts trigger context for workflow template expansion
// from a GitLab issue, mirroring the extractIssueContext pattern from poller.go.
func (e *GitLabEnricher) ExtractGitLabIssueContext(ctx context.Context, token, apiHost, project, issueIID string) map[string]interface{} {
	// URL-encode the project path
	encodedProject := url.PathEscape(project)

	// Fetch issue details
	issueURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%s", apiHost, encodedProject, issueIID)
	data, err := e.gitlabAPIGet(ctx, token, issueURL)
	if err != nil {
		log.Printf("enrichment: could not fetch issue #%s for context extraction: %v", issueIID, err)
		return nil
	}

	var issue struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		State       string   `json:"state"`
		Author      struct {
			Username string `json:"username"`
		} `json:"author"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		log.Printf("enrichment: error parsing issue #%s for context extraction: %v", issueIID, err)
		return nil
	}

	return map[string]interface{}{
		"issue_id":          issueIID,
		"issue_title":       issue.Title,
		"issue_description": issue.Description,
		"issue_state":       issue.State,
		"issue_author":      issue.Author.Username,
		"issue_labels":      strings.Join(issue.Labels, ","),
		"project":           project,
	}
}