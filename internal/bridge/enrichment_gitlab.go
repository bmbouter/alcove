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

// enrichMRContext fetches and formats GitLab merge request details, notes, diff stats, CI jobs, and approvals.
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
		Assignees []struct {
			Username string `json:"username"`
		} `json:"assignees"`
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

	if len(mr.Assignees) > 0 {
		var assigneeNames []string
		for _, a := range mr.Assignees {
			assigneeNames = append(assigneeNames, "@"+a.Username)
		}
		sb.WriteString(fmt.Sprintf("**Assignees**: %s\n", strings.Join(assigneeNames, ", ")))
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

	// Fetch diff stats
	e.enrichMRDiffStats(ctx, token, apiHost, encodedProject, mrIID, sb)

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

	// Fetch pipeline jobs with detailed CI status
	e.enrichMRPipelineJobs(ctx, token, apiHost, encodedProject, mrIID, sb)

	// Fetch MR approvals
	e.enrichMRApprovals(ctx, token, apiHost, encodedProject, mrIID, sb)
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
		Labels    []string `json:"labels"`
		Assignees []struct {
			Username string `json:"username"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		log.Printf("enrichment: error parsing issue #%s for context extraction: %v", issueIID, err)
		return nil
	}

	var assigneeNames []string
	for _, a := range issue.Assignees {
		assigneeNames = append(assigneeNames, a.Username)
	}

	return map[string]interface{}{
		"issue_id":          issueIID,
		"issue_title":       issue.Title,
		"issue_description": issue.Description,
		"issue_body":        issue.Description, // Alias for compatibility
		"issue_state":       issue.State,
		"issue_author":      issue.Author.Username,
		"issue_assignees":   strings.Join(assigneeNames, ","),
		"issue_labels":      strings.Join(issue.Labels, ","),
		"project":           project,
	}
}

// ExtractGitLabMRContext extracts trigger context for workflow template expansion
// from a GitLab merge request.
func (e *GitLabEnricher) ExtractGitLabMRContext(ctx context.Context, token, apiHost, project, mrIID string) map[string]interface{} {
	// URL-encode the project path
	encodedProject := url.PathEscape(project)

	// Fetch MR details
	mrURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s", apiHost, encodedProject, mrIID)
	data, err := e.gitlabAPIGet(ctx, token, mrURL)
	if err != nil {
		log.Printf("enrichment: could not fetch MR !%s for context extraction: %v", mrIID, err)
		return nil
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
		Labels    []string `json:"labels"`
		Assignees []struct {
			Username string `json:"username"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal(data, &mr); err != nil {
		log.Printf("enrichment: error parsing MR !%s for context extraction: %v", mrIID, err)
		return nil
	}

	var assigneeNames []string
	for _, a := range mr.Assignees {
		assigneeNames = append(assigneeNames, a.Username)
	}

	return map[string]interface{}{
		"mr_iid":          mrIID,
		"mr_title":        mr.Title,
		"mr_description":  mr.Description,
		"mr_body":         mr.Description, // Alias for compatibility
		"mr_state":        mr.State,
		"mr_author":       mr.Author.Username,
		"mr_source_branch": mr.SourceBranch,
		"mr_target_branch": mr.TargetBranch,
		"mr_labels":       strings.Join(mr.Labels, ","),
		"mr_assignees":    strings.Join(assigneeNames, ","),
		"project":         project,
	}
}

// enrichMRDiffStats fetches and formats MR diff statistics.
func (e *GitLabEnricher) enrichMRDiffStats(ctx context.Context, token, apiHost, encodedProject, mrIID string, sb *strings.Builder) {
	// Fetch MR changes (diff stats)
	changesURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/changes", apiHost, encodedProject, mrIID)
	changesData, err := e.gitlabAPIGet(ctx, token, changesURL)
	if err != nil {
		log.Printf("enrichment: could not fetch MR diff stats: %v", err)
		return
	}

	var changes struct {
		ChangesCount string `json:"changes_count"`
		Changes      []struct {
			OldPath     string `json:"old_path"`
			NewPath     string `json:"new_path"`
			DeletedFile bool   `json:"deleted_file"`
			NewFile     bool   `json:"new_file"`
			Diff        string `json:"diff"`
		} `json:"changes"`
	}

	if err := json.Unmarshal(changesData, &changes); err != nil {
		log.Printf("enrichment: error parsing MR diff stats: %v", err)
		return
	}

	if len(changes.Changes) == 0 {
		return // No changes to report
	}

	// Count files, additions, and deletions
	filesChanged := len(changes.Changes)
	var additions, deletions int
	var changedFiles []string

	for i, change := range changes.Changes {
		if i < 10 { // Limit to top 10 files for orientation
			filename := change.NewPath
			if change.DeletedFile {
				filename = change.OldPath + " (deleted)"
			} else if change.NewFile {
				filename = change.NewPath + " (new)"
			}
			changedFiles = append(changedFiles, filename)
		}

		// Parse diff for line count (simplified - count +/- lines)
		lines := strings.Split(change.Diff, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				additions++
			} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				deletions++
			}
		}
	}

	sb.WriteString(fmt.Sprintf("\n### Changed Files\n\n"))
	sb.WriteString(fmt.Sprintf("**Changed files**: %d (+%d, -%d)\n\n", filesChanged, additions, deletions))

	if len(changedFiles) > 0 {
		sb.WriteString("**Files**:\n")
		for _, file := range changedFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", file))
		}
		if len(changes.Changes) > 10 {
			sb.WriteString(fmt.Sprintf("- ... and %d more files\n", len(changes.Changes)-10))
		}
	}
}

// enrichMRPipelineJobs fetches and formats detailed CI pipeline job status and failure logs.
func (e *GitLabEnricher) enrichMRPipelineJobs(ctx context.Context, token, apiHost, encodedProject, mrIID string, sb *strings.Builder) {
	// Fetch pipelines for the MR
	pipelinesURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/pipelines", apiHost, encodedProject, mrIID)
	pipelineData, err := e.gitlabAPIGet(ctx, token, pipelinesURL)
	if err != nil {
		log.Printf("enrichment: could not fetch pipelines: %v", err)
		return
	}

	var pipelines []struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(pipelineData, &pipelines); err != nil || len(pipelines) == 0 {
		return // No pipelines or parse error
	}

	// Get the latest pipeline
	latest := pipelines[0] // GitLab returns most recent first

	// Fetch jobs for the latest pipeline
	jobsURL := fmt.Sprintf("%s/api/v4/projects/%s/pipelines/%d/jobs", apiHost, encodedProject, latest.ID)
	jobsData, err := e.gitlabAPIGet(ctx, token, jobsURL)
	if err != nil {
		// Fall back to basic pipeline status
		sb.WriteString("\n### Pipeline Status\n\n")
		sb.WriteString(fmt.Sprintf("- Pipeline #%d (%s)\n", latest.ID, latest.Status))
		return
	}

	var jobs []struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Stage  string `json:"stage"`
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(jobsData, &jobs); err != nil {
		// Fall back to basic pipeline status
		sb.WriteString("\n### Pipeline Status\n\n")
		sb.WriteString(fmt.Sprintf("- Pipeline #%d (%s)\n", latest.ID, latest.Status))
		return
	}

	sb.WriteString(fmt.Sprintf("\n### CI Status\n\n"))
	sb.WriteString(fmt.Sprintf("**Pipeline #%d** (%s)\n\n", latest.ID, latest.Status))

	if len(jobs) > 0 {
		// Group jobs by stage
		stageJobs := make(map[string][]struct {
			ID     int
			Name   string
			Status string
		})

		for _, job := range jobs {
			stageJobs[job.Stage] = append(stageJobs[job.Stage], struct {
				ID     int
				Name   string
				Status string
			}{job.ID, job.Name, job.Status})
		}

		// List jobs by stage
		for stage, stageJobList := range stageJobs {
			sb.WriteString(fmt.Sprintf("**%s:**\n", stage))
			for _, job := range stageJobList {
				sb.WriteString(fmt.Sprintf("- %s (%s)\n", job.Name, job.Status))

				// Fetch failure logs for failed jobs
				if job.Status == "failed" {
					e.fetchJobFailureLog(ctx, token, apiHost, encodedProject, job.ID, job.Name, sb)
				}
			}
			sb.WriteString("\n")
		}
	}
}

// fetchJobFailureLog fetches trace logs for failed CI jobs (capped at maxCILogLen).
func (e *GitLabEnricher) fetchJobFailureLog(ctx context.Context, token, apiHost, encodedProject string, jobID int, jobName string, sb *strings.Builder) {
	traceURL := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/trace", apiHost, encodedProject, jobID)

	// Create custom request for trace endpoint (returns plain text, not JSON)
	req, err := http.NewRequestWithContext(ctx, "GET", traceURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("User-Agent", "alcove-enricher")

	resp, err := e.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// Check if it's actually text content
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "text/") && !strings.Contains(contentType, "application/octet-stream") {
		// Skip binary content
		return
	}

	if resp.StatusCode == 403 || resp.StatusCode == 404 {
		// Private project logs or job not found
		return
	}

	if resp.StatusCode != http.StatusOK {
		return
	}

	// Read trace content with size limit
	traceContent, err := io.ReadAll(io.LimitReader(resp.Body, maxCILogLen))
	if err != nil {
		return
	}

	trace := string(traceContent)
	if trace == "" {
		return
	}

	// Trim to maxCILogLen if needed and truncate marker
	if len(trace) >= maxCILogLen {
		trace = trace[:maxCILogLen-20] + "\n... (log truncated)"
	}

	sb.WriteString(fmt.Sprintf("\n**%s failure log:**\n```\n%s\n```\n", jobName, trace))
}

// enrichMRApprovals fetches and formats MR approval status and reviewers.
func (e *GitLabEnricher) enrichMRApprovals(ctx context.Context, token, apiHost, encodedProject, mrIID string, sb *strings.Builder) {
	// Fetch MR approvals (GitLab Premium feature)
	approvalsURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%s/approvals", apiHost, encodedProject, mrIID)
	approvalsData, err := e.gitlabAPIGet(ctx, token, approvalsURL)
	if err != nil {
		// Approvals API may not be available (GitLab CE/Free) - silently skip
		return
	}

	var approvals struct {
		ApprovalsRequired int `json:"approvals_required"`
		ApprovalsLeft     int `json:"approvals_left"`
		ApprovedBy        []struct {
			User struct {
				Username string `json:"username"`
			} `json:"user"`
		} `json:"approved_by"`
		SuggestedApprovers []struct {
			Username string `json:"username"`
		} `json:"suggested_approvers"`
	}

	if err := json.Unmarshal(approvalsData, &approvals); err != nil {
		return // Parse error - skip approvals section
	}

	// Only show approvals section if there's meaningful approval info
	if approvals.ApprovalsRequired > 0 || len(approvals.ApprovedBy) > 0 {
		sb.WriteString("\n### Approvals\n\n")

		if approvals.ApprovalsRequired > 0 {
			sb.WriteString(fmt.Sprintf("**Required approvals**: %d\n", approvals.ApprovalsRequired))
			sb.WriteString(fmt.Sprintf("**Approvals remaining**: %d\n", approvals.ApprovalsLeft))
		}

		if len(approvals.ApprovedBy) > 0 {
			var approvers []string
			for _, approval := range approvals.ApprovedBy {
				approvers = append(approvers, "@"+approval.User.Username)
			}
			sb.WriteString(fmt.Sprintf("**Approved by**: %s\n", strings.Join(approvers, ", ")))
		}

		if len(approvals.SuggestedApprovers) > 0 {
			var suggested []string
			for _, approver := range approvals.SuggestedApprovers {
				suggested = append(suggested, "@"+approver.Username)
			}
			sb.WriteString(fmt.Sprintf("**Suggested approvers**: %s\n", strings.Join(suggested, ", ")))
		}
	}
}