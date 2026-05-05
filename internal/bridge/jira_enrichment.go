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
	"log"
	"strings"
)

// jiraFullIssue represents the JIRA issue response with expanded fields
type jiraFullIssue struct {
	Key    string `json:"key"`
	Fields struct {
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
	} `json:"fields"`
}

// jiraComments represents the JIRA comments response
type jiraComments struct {
	Comments []struct {
		Body   string `json:"body"`
		Author struct {
			DisplayName  string `json:"displayName"`
			EmailAddress string `json:"emailAddress"`
		} `json:"author"`
		Created string `json:"created"`
	} `json:"comments"`
}

// jiraAgileIssue represents the JIRA agile API response for sprint context
type jiraAgileIssue struct {
	Fields struct {
		Sprint *struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"sprint"`
	} `json:"fields"`
}

// enrichJiraIssueContext fetches rich context from JIRA for the given issue
// and returns a structured markdown preamble string that provides the LLM with
// full issue details without needing to make its own API calls.
func (jp *JiraPoller) enrichJiraIssueContext(ctx context.Context, token, issueKey string) string {
	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString(fmt.Sprintf("**Event**: jira / issue_updated\n"))
	sb.WriteString(fmt.Sprintf("**Issue Key**: %s\n", issueKey))
	sb.WriteString(fmt.Sprintf("**URL**: %s/browse/%s\n", jp.baseURL, issueKey))
	sb.WriteString("\n")

	// Fetch full issue details
	fullIssue, err := jp.fetchFullIssue(ctx, token, issueKey)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch full issue %s: %v", issueKey, err)
		sb.WriteString(fmt.Sprintf("Could not fetch full issue details: %v\n", err))
		return sb.String() + "\n---\n"
	}

	// Write issue details
	jp.writeIssueDetails(&sb, fullIssue)

	// Fetch and write comments (graceful degradation)
	jp.writeComments(ctx, token, issueKey, &sb)

	// Fetch and write sprint context (graceful degradation)
	jp.writeSprintContext(ctx, token, issueKey, &sb)

	sb.WriteString("\n---\n")
	return sb.String()
}

// enrichJiraIssueContextWithData fetches rich context and also returns the structured data
// for use in building the trigger context.
func (jp *JiraPoller) enrichJiraIssueContextWithData(ctx context.Context, token, issueKey string) (string, *jiraComments, *jiraAgileIssue) {
	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString(fmt.Sprintf("**Event**: jira / issue_updated\n"))
	sb.WriteString(fmt.Sprintf("**Issue Key**: %s\n", issueKey))
	sb.WriteString(fmt.Sprintf("**URL**: %s/browse/%s\n", jp.baseURL, issueKey))
	sb.WriteString("\n")

	// Fetch full issue details
	fullIssue, err := jp.fetchFullIssue(ctx, token, issueKey)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch full issue %s: %v", issueKey, err)
		sb.WriteString(fmt.Sprintf("Could not fetch full issue details: %v\n", err))
		return sb.String() + "\n---\n", nil, nil
	}

	// Write issue details
	jp.writeIssueDetails(&sb, fullIssue)

	// Fetch and write comments
	comments := jp.fetchAndWriteComments(ctx, token, issueKey, &sb)

	// Fetch and write sprint context
	sprintInfo := jp.fetchAndWriteSprintContext(ctx, token, issueKey, &sb)

	sb.WriteString("\n---\n")
	return sb.String(), comments, sprintInfo
}

// fetchFullIssue retrieves the full issue details with all required fields
func (jp *JiraPoller) fetchFullIssue(ctx context.Context, token, issueKey string) (*jiraFullIssue, error) {
	fields := "summary,description,status,issuetype,priority,assignee,reporter,components,labels,issuelinks,attachment"
	issueURL := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=%s", jp.baseURL, issueKey, fields)

	data, err := jp.jiraRequest(ctx, token, "GET", issueURL, nil)
	if err != nil {
		return nil, fmt.Errorf("issue fetch failed: %w", err)
	}

	var issue jiraFullIssue
	if err := json.Unmarshal(data, &issue); err != nil {
		return nil, fmt.Errorf("issue parse failed: %w", err)
	}

	return &issue, nil
}

// writeIssueDetails writes the core issue information to the string builder
func (jp *JiraPoller) writeIssueDetails(sb *strings.Builder, issue *jiraFullIssue) {
	sb.WriteString(fmt.Sprintf("### Issue: %s\n\n", issue.Fields.Summary))
	sb.WriteString(fmt.Sprintf("**Status**: %s\n", issue.Fields.Status.Name))
	sb.WriteString(fmt.Sprintf("**Type**: %s\n", issue.Fields.IssueType.Name))

	if issue.Fields.Priority.Name != "" {
		sb.WriteString(fmt.Sprintf("**Priority**: %s\n", issue.Fields.Priority.Name))
	}

	if issue.Fields.Assignee != nil {
		sb.WriteString(fmt.Sprintf("**Assignee**: %s\n", issue.Fields.Assignee.DisplayName))
	} else {
		sb.WriteString("**Assignee**: Unassigned\n")
	}

	sb.WriteString(fmt.Sprintf("**Reporter**: %s\n", issue.Fields.Reporter.DisplayName))

	if len(issue.Fields.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(issue.Fields.Labels, ", ")))
	}

	if len(issue.Fields.Components) > 0 {
		var componentNames []string
		for _, c := range issue.Fields.Components {
			componentNames = append(componentNames, c.Name)
		}
		sb.WriteString(fmt.Sprintf("**Components**: %s\n", strings.Join(componentNames, ", ")))
	}

	// Description
	sb.WriteString("\n**Description**:\n")
	description := issue.Fields.Description
	if len(description) > maxBodyLen {
		description = description[:maxBodyLen] + "\n... (truncated)"
	}
	if description == "" {
		description = "(empty)"
	}
	sb.WriteString(description + "\n")

	// Linked issues
	if len(issue.Fields.IssueLinks) > 0 {
		sb.WriteString("\n### Linked Issues\n\n")
		for _, link := range issue.Fields.IssueLinks {
			if link.OutwardIssue != nil {
				sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", link.Type.Outward, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary))
			}
			if link.InwardIssue != nil {
				sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", link.Type.Inward, link.InwardIssue.Key, link.InwardIssue.Fields.Summary))
			}
		}
	}

	// Attachments metadata
	if len(issue.Fields.Attachment) > 0 {
		sb.WriteString("\n### Attachments\n\n")
		for _, att := range issue.Fields.Attachment {
			sizeStr := formatFileSize(att.Size)
			sb.WriteString(fmt.Sprintf("- %s (%s, %s)\n", att.Filename, sizeStr, att.MimeType))
		}
	}
}

// fetchAndWriteComments fetches and writes comments for the issue, returning the parsed data
func (jp *JiraPoller) fetchAndWriteComments(ctx context.Context, token, issueKey string, sb *strings.Builder) *jiraComments {
	commentsURL := fmt.Sprintf("%s/rest/api/2/issue/%s/comment?maxResults=%d&orderBy=-created", jp.baseURL, issueKey, maxCommentsNum)

	data, err := jp.jiraRequest(ctx, token, "GET", commentsURL, nil)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch comments for %s: %v", issueKey, err)
		return nil
	}

	var comments jiraComments
	if err := json.Unmarshal(data, &comments); err != nil {
		log.Printf("jira-enrichment: error parsing comments for %s: %v", issueKey, err)
		return nil
	}

	if len(comments.Comments) > 0 {
		sb.WriteString(fmt.Sprintf("\n### Comments (%d)\n\n", len(comments.Comments)))
		for _, c := range comments.Comments {
			comment := c.Body
			if len(comment) > maxCommentLen {
				comment = comment[:maxCommentLen] + "\n... (truncated)"
			}
			if comment == "" {
				comment = "(empty)"
			}

			// Extract date from timestamp (format: 2026-01-15T10:00:00.000Z)
			dateStr := c.Created
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}

			sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n", c.Author.DisplayName, dateStr, comment))
		}
	}

	return &comments
}

// fetchAndWriteSprintContext fetches and writes sprint information, returning the parsed data
func (jp *JiraPoller) fetchAndWriteSprintContext(ctx context.Context, token, issueKey string, sb *strings.Builder) *jiraAgileIssue {
	sprintURL := fmt.Sprintf("%s/rest/agile/1.0/issue/%s?fields=sprint", jp.baseURL, issueKey)

	data, err := jp.jiraRequest(ctx, token, "GET", sprintURL, nil)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch sprint for %s: %v", issueKey, err)
		return nil
	}

	var agileIssue jiraAgileIssue
	if err := json.Unmarshal(data, &agileIssue); err != nil {
		log.Printf("jira-enrichment: error parsing agile issue for %s: %v", issueKey, err)
		return nil
	}

	if agileIssue.Fields.Sprint != nil {
		sb.WriteString(fmt.Sprintf("\n### Sprint\n\n"))
		sb.WriteString(fmt.Sprintf("**Name**: %s\n", agileIssue.Fields.Sprint.Name))
		sb.WriteString(fmt.Sprintf("**State**: %s\n", agileIssue.Fields.Sprint.State))
	}

	return &agileIssue
}

// writeComments fetches and writes comments for the issue (for basic enrichment)
func (jp *JiraPoller) writeComments(ctx context.Context, token, issueKey string, sb *strings.Builder) {
	comments := jp.fetchAndWriteComments(ctx, token, issueKey, sb)
	_ = comments // We only need the side effect of writing to sb
}

// writeSprintContext fetches and writes sprint information (for basic enrichment)
func (jp *JiraPoller) writeSprintContext(ctx context.Context, token, issueKey string, sb *strings.Builder) {
	sprintInfo := jp.fetchAndWriteSprintContext(ctx, token, issueKey, sb)
	_ = sprintInfo // We only need the side effect of writing to sb
}

// buildJiraTriggerContext builds an expanded trigger context map that includes
// all the basic fields plus enriched data for template variables
func (jp *JiraPoller) buildJiraTriggerContext(issue *jiraFullIssue, enrichedMarkdown string) map[string]interface{} {
	return jp.buildJiraTriggerContextWithData(issue, enrichedMarkdown, nil, nil)
}

// buildJiraTriggerContextWithData builds an expanded trigger context map with additional
// structured data from comments and sprint information
func (jp *JiraPoller) buildJiraTriggerContextWithData(issue *jiraFullIssue, enrichedMarkdown string, comments *jiraComments, sprintInfo *jiraAgileIssue) map[string]interface{} {
	triggerContext := map[string]interface{}{
		// Existing basic fields
		"issue_key":    issue.Key,
		"issue_title":  issue.Fields.Summary,
		"issue_body":   issue.Fields.Description,
		"issue_url":    fmt.Sprintf("%s/browse/%s", jp.baseURL, issue.Key),
		"issue_status": issue.Fields.Status.Name,
		"issue_labels": issue.Fields.Labels,
		"issue_type":   issue.Fields.IssueType.Name,

		// New enriched fields
		"enriched_context": enrichedMarkdown,
		"issue_priority":   issue.Fields.Priority.Name,
		"issue_reporter":   issue.Fields.Reporter.DisplayName,
	}

	// Handle assignee (can be null)
	if issue.Fields.Assignee != nil {
		triggerContext["issue_assignee"] = issue.Fields.Assignee.DisplayName
	} else {
		triggerContext["issue_assignee"] = "Unassigned"
	}

	// Format linked issues as text
	var linkedIssuesList []string
	for _, link := range issue.Fields.IssueLinks {
		if link.OutwardIssue != nil {
			linkedIssuesList = append(linkedIssuesList, fmt.Sprintf("[%s] %s: %s", link.Type.Outward, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary))
		}
		if link.InwardIssue != nil {
			linkedIssuesList = append(linkedIssuesList, fmt.Sprintf("[%s] %s: %s", link.Type.Inward, link.InwardIssue.Key, link.InwardIssue.Fields.Summary))
		}
	}
	triggerContext["issue_linked_issues"] = strings.Join(linkedIssuesList, "\n")

	// Format attachment filenames
	var attachmentNames []string
	for _, att := range issue.Fields.Attachment {
		attachmentNames = append(attachmentNames, att.Filename)
	}
	triggerContext["issue_attachments"] = strings.Join(attachmentNames, ", ")

	// Format comments for template usage
	var commentsFormatted []string
	if comments != nil {
		for _, c := range comments.Comments {
			dateStr := c.Created
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}
			comment := c.Body
			if len(comment) > maxCommentLen {
				comment = comment[:maxCommentLen] + " ... (truncated)"
			}
			commentsFormatted = append(commentsFormatted, fmt.Sprintf("%s (%s): %s", c.Author.DisplayName, dateStr, comment))
		}
	}
	triggerContext["issue_comments"] = strings.Join(commentsFormatted, "\n\n")

	// Format sprint information
	if sprintInfo != nil && sprintInfo.Fields.Sprint != nil {
		triggerContext["issue_sprint"] = fmt.Sprintf("%s (%s)", sprintInfo.Fields.Sprint.Name, sprintInfo.Fields.Sprint.State)
	} else {
		triggerContext["issue_sprint"] = ""
	}

	return triggerContext
}

// formatFileSize converts bytes to human-readable format
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
