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

// JiraIssue represents a full JIRA issue with all fields needed for enrichment
type JiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
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
	} `json:"fields"`
}

// JiraComments represents the comments response from JIRA
type JiraComments struct {
	Comments []struct {
		Body   string `json:"body"`
		Author struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Created string `json:"created"`
	} `json:"comments"`
}

// JiraAgileSprint represents sprint info from the agile API
type JiraAgileSprint struct {
	Sprint []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"sprint"`
}

// enrichJiraIssueContext fetches rich context from JIRA for the given issue key
// and returns both a structured markdown preamble and additional context fields.
func (jp *JiraPoller) enrichJiraIssueContext(ctx context.Context, token, issueKey string) (string, map[string]interface{}) {
	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString("**Event**: JIRA Issue Update\n")
	sb.WriteString(fmt.Sprintf("**Issue**: %s\n\n", issueKey))

	additionalContext := make(map[string]interface{})

	// Fetch full issue details
	issueURL := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=summary,description,status,issuetype,priority,assignee,reporter,components,labels,issuelinks,attachment",
		jp.baseURL, issueKey)

	issueData, err := jp.jiraRequest(ctx, token, "GET", issueURL, nil)
	if err != nil {
		log.Printf("jira-poller: enrichment: could not fetch issue %s: %v", issueKey, err)
		sb.WriteString(fmt.Sprintf("Could not fetch issue %s: %v\n", issueKey, err))
		return sb.String(), additionalContext
	}

	var issue JiraIssue
	if err := json.Unmarshal(issueData, &issue); err != nil {
		log.Printf("jira-poller: enrichment: error parsing issue %s: %v", issueKey, err)
		sb.WriteString(fmt.Sprintf("Could not parse issue %s: %v\n", issueKey, err))
		return sb.String(), additionalContext
	}

	// Basic issue information
	sb.WriteString(fmt.Sprintf("### Issue %s: %s\n\n", issue.Key, issue.Fields.Summary))
	sb.WriteString(fmt.Sprintf("**Status**: %s\n", issue.Fields.Status.Name))
	sb.WriteString(fmt.Sprintf("**Type**: %s\n", issue.Fields.IssueType.Name))

	if issue.Fields.Priority.Name != "" {
		sb.WriteString(fmt.Sprintf("**Priority**: %s\n", issue.Fields.Priority.Name))
		additionalContext["issue_priority"] = issue.Fields.Priority.Name
	}

	if issue.Fields.Assignee.DisplayName != "" {
		sb.WriteString(fmt.Sprintf("**Assignee**: %s\n", issue.Fields.Assignee.DisplayName))
		additionalContext["issue_assignee"] = issue.Fields.Assignee.DisplayName
	}

	if issue.Fields.Reporter.DisplayName != "" {
		sb.WriteString(fmt.Sprintf("**Reporter**: %s\n", issue.Fields.Reporter.DisplayName))
		additionalContext["issue_reporter"] = issue.Fields.Reporter.DisplayName
	}

	// Components and labels
	if len(issue.Fields.Components) > 0 {
		var componentNames []string
		for _, c := range issue.Fields.Components {
			componentNames = append(componentNames, c.Name)
		}
		componentStr := strings.Join(componentNames, ", ")
		sb.WriteString(fmt.Sprintf("**Components**: %s\n", componentStr))
		additionalContext["issue_components"] = componentStr
	}

	if len(issue.Fields.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(issue.Fields.Labels, ", ")))
	}

	// Description
	sb.WriteString("\n**Description**:\n")
	description := extractADFText(issue.Fields.Description)
	if len(description) > maxBodyLen {
		description = description[:maxBodyLen] + "\n... (truncated)"
	}
	if description == "" {
		description = "(empty)"
	}
	sb.WriteString(description + "\n")

	// Fetch comments
	commentsText := jp.enrichJiraComments(ctx, token, issueKey, &sb)
	if commentsText != "" {
		additionalContext["issue_comments"] = commentsText
	}

	// Fetch sprint information
	sprintText := jp.enrichJiraSprintContext(ctx, token, issueKey, &sb)
	if sprintText != "" {
		additionalContext["issue_sprint"] = sprintText
	}

	// Process linked issues
	linkedIssuesText := jp.enrichJiraLinkedIssues(&issue, &sb)
	if linkedIssuesText != "" {
		additionalContext["issue_linked_issues"] = linkedIssuesText
	}

	// Process attachments
	attachmentsText := jp.enrichJiraAttachments(&issue, &sb)
	if attachmentsText != "" {
		additionalContext["issue_attachments"] = attachmentsText
	}

	sb.WriteString("\n---\n")
	return sb.String(), additionalContext
}

// enrichJiraComments fetches and formats comments for the issue
func (jp *JiraPoller) enrichJiraComments(ctx context.Context, token, issueKey string, sb *strings.Builder) string {
	commentsURL := fmt.Sprintf("%s/rest/api/2/issue/%s/comment?maxResults=%d&orderBy=-created",
		jp.baseURL, issueKey, maxCommentsNum)

	commentsData, err := jp.jiraRequest(ctx, token, "GET", commentsURL, nil)
	if err != nil {
		log.Printf("jira-poller: enrichment: could not fetch comments for issue %s: %v", issueKey, err)
		return ""
	}

	var comments JiraComments
	if err := json.Unmarshal(commentsData, &comments); err != nil {
		log.Printf("jira-poller: enrichment: error parsing comments for issue %s: %v", issueKey, err)
		return ""
	}

	if len(comments.Comments) == 0 {
		return ""
	}

	sb.WriteString(fmt.Sprintf("\n### Comments (%d)\n\n", len(comments.Comments)))

	var commentsText []string
	for _, c := range comments.Comments {
		comment := c.Body
		if len(comment) > maxCommentLen {
			comment = comment[:maxCommentLen] + "\n... (truncated)"
		}
		dateStr := c.Created
		if len(dateStr) >= 10 {
			dateStr = dateStr[:10]
		}
		commentFormatted := fmt.Sprintf("%s (%s): %s", c.Author.DisplayName, dateStr, comment)
		commentsText = append(commentsText, commentFormatted)
		sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n", c.Author.DisplayName, dateStr, comment))
	}

	return strings.Join(commentsText, "\n")
}

// enrichJiraSprintContext fetches sprint information using the agile API
func (jp *JiraPoller) enrichJiraSprintContext(ctx context.Context, token, issueKey string, sb *strings.Builder) string {
	sprintURL := fmt.Sprintf("%s/rest/agile/1.0/issue/%s?fields=sprint", jp.baseURL, issueKey)

	sprintData, err := jp.jiraRequest(ctx, token, "GET", sprintURL, nil)
	if err != nil {
		log.Printf("jira-poller: enrichment: could not fetch sprint for issue %s (agile API may not be available): %v", issueKey, err)
		return ""
	}

	var sprint JiraAgileSprint
	if err := json.Unmarshal(sprintData, &sprint); err != nil {
		log.Printf("jira-poller: enrichment: error parsing sprint for issue %s: %v", issueKey, err)
		return ""
	}

	if len(sprint.Sprint) == 0 {
		return ""
	}

	sb.WriteString("\n### Sprint Context\n\n")
	var sprintTexts []string
	for _, s := range sprint.Sprint {
		sprintText := fmt.Sprintf("%s (%s)", s.Name, s.State)
		sprintTexts = append(sprintTexts, sprintText)
		sb.WriteString(fmt.Sprintf("- **%s** (%s)\n", s.Name, s.State))
	}

	return strings.Join(sprintTexts, ", ")
}

// enrichJiraLinkedIssues processes and formats linked issues
func (jp *JiraPoller) enrichJiraLinkedIssues(issue *JiraIssue, sb *strings.Builder) string {
	if len(issue.Fields.IssueLinks) == 0 {
		return ""
	}

	sb.WriteString("\n### Linked Issues\n\n")
	var linkedIssuesText []string

	for _, link := range issue.Fields.IssueLinks {
		if link.OutwardIssue.Key != "" {
			linkText := fmt.Sprintf("[%s] %s: %s",
				link.Type.Outward, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary)
			linkedIssuesText = append(linkedIssuesText, linkText)
			sb.WriteString(fmt.Sprintf("- %s\n", linkText))
		}
		if link.InwardIssue.Key != "" {
			linkText := fmt.Sprintf("[%s] %s: %s",
				link.Type.Inward, link.InwardIssue.Key, link.InwardIssue.Fields.Summary)
			linkedIssuesText = append(linkedIssuesText, linkText)
			sb.WriteString(fmt.Sprintf("- %s\n", linkText))
		}
	}

	return strings.Join(linkedIssuesText, "\n")
}

// enrichJiraAttachments processes and formats attachment metadata
func (jp *JiraPoller) enrichJiraAttachments(issue *JiraIssue, sb *strings.Builder) string {
	if len(issue.Fields.Attachment) == 0 {
		return ""
	}

	sb.WriteString("\n### Attachments\n\n")
	var attachmentNames []string

	for _, att := range issue.Fields.Attachment {
		sizeStr := formatFileSize(att.Size)
		sb.WriteString(fmt.Sprintf("- %s (%s, %s)\n", att.Filename, sizeStr, att.MimeType))
		attachmentNames = append(attachmentNames, att.Filename)
	}

	return strings.Join(attachmentNames, ", ")
}

// buildJiraTriggerContext creates an expanded trigger context map with all available fields
func (jp *JiraPoller) buildJiraTriggerContext(issue *JiraIssue, enrichedMarkdown string) map[string]interface{} {
	context := map[string]interface{}{
		"issue_key":        issue.Key,
		"issue_title":      issue.Fields.Summary,
		"issue_body":       extractADFText(issue.Fields.Description),
		"issue_url":        fmt.Sprintf("%s/browse/%s", jp.baseURL, issue.Key),
		"issue_status":     issue.Fields.Status.Name,
		"issue_labels":     issue.Fields.Labels,
		"issue_type":       issue.Fields.IssueType.Name,
		"enriched_context": enrichedMarkdown,
	}

	// Add priority if available
	if issue.Fields.Priority.Name != "" {
		context["issue_priority"] = issue.Fields.Priority.Name
	}

	// Add assignee if available
	if issue.Fields.Assignee.DisplayName != "" {
		context["issue_assignee"] = issue.Fields.Assignee.DisplayName
	}

	// Add reporter if available
	if issue.Fields.Reporter.DisplayName != "" {
		context["issue_reporter"] = issue.Fields.Reporter.DisplayName
	}

	// Format components
	var componentNames []string
	for _, c := range issue.Fields.Components {
		componentNames = append(componentNames, c.Name)
	}
	if len(componentNames) > 0 {
		context["issue_components"] = strings.Join(componentNames, ", ")
	}

	// Format linked issues
	var linkedIssuesText []string
	for _, link := range issue.Fields.IssueLinks {
		if link.OutwardIssue.Key != "" {
			linkedIssuesText = append(linkedIssuesText, fmt.Sprintf("[%s] %s: %s",
				link.Type.Outward, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary))
		}
		if link.InwardIssue.Key != "" {
			linkedIssuesText = append(linkedIssuesText, fmt.Sprintf("[%s] %s: %s",
				link.Type.Inward, link.InwardIssue.Key, link.InwardIssue.Fields.Summary))
		}
	}
	if len(linkedIssuesText) > 0 {
		context["issue_linked_issues"] = strings.Join(linkedIssuesText, "\n")
	}

	// Format attachments
	var attachmentNames []string
	for _, att := range issue.Fields.Attachment {
		attachmentNames = append(attachmentNames, att.Filename)
	}
	if len(attachmentNames) > 0 {
		context["issue_attachments"] = strings.Join(attachmentNames, ", ")
	}

	return context
}

// formatFileSize converts bytes to a human-readable format
func formatFileSize(bytes int) string {
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
