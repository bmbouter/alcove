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

// enrichJiraIssueContext fetches rich context from JIRA for the given issue
// and returns a structured preamble string that provides the LLM with
// full issue details without needing to make its own API calls.
// It also returns the structured data for use in buildJiraTriggerContext.
func (jp *JiraPoller) enrichJiraIssueContext(ctx context.Context, token, issueKey string) (string, *enrichmentData, error) {
	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString(fmt.Sprintf("**Event**: JIRA Issue Update\n"))
	sb.WriteString(fmt.Sprintf("**Issue**: %s\n\n", issueKey))

	// Fetch full issue details
	issue, err := jp.fetchFullIssue(ctx, token, issueKey)
	if err != nil {
		log.Printf("jira-enrichment: error fetching issue %s: %v", issueKey, err)
		sb.WriteString(fmt.Sprintf("Could not fetch issue %s: %v\n", issueKey, err))
		return sb.String(), nil, err
	}

	// Basic issue information
	sb.WriteString(fmt.Sprintf("### Issue %s: %s\n\n", issueKey, issue.Fields.Summary))
	sb.WriteString(fmt.Sprintf("**Status**: %s\n", issue.Fields.Status.Name))
	sb.WriteString(fmt.Sprintf("**Type**: %s\n", issue.Fields.IssueType.Name))

	if issue.Fields.Priority.Name != "" {
		sb.WriteString(fmt.Sprintf("**Priority**: %s\n", issue.Fields.Priority.Name))
	}
	if issue.Fields.Assignee != nil && issue.Fields.Assignee.DisplayName != "" {
		sb.WriteString(fmt.Sprintf("**Assignee**: %s\n", issue.Fields.Assignee.DisplayName))
	}
	if issue.Fields.Reporter != nil && issue.Fields.Reporter.DisplayName != "" {
		sb.WriteString(fmt.Sprintf("**Reporter**: %s\n", issue.Fields.Reporter.DisplayName))
	}

	// Components and labels
	if len(issue.Fields.Components) > 0 {
		var componentNames []string
		for _, c := range issue.Fields.Components {
			componentNames = append(componentNames, c.Name)
		}
		sb.WriteString(fmt.Sprintf("**Components**: %s\n", strings.Join(componentNames, ", ")))
	}
	if len(issue.Fields.Labels) > 0 {
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(issue.Fields.Labels, ", ")))
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

	// Prepare enrichment data structure
	enrichData := &enrichmentData{
		issue: issue,
	}

	// Comments
	comments := jp.enrichIssueComments(ctx, token, issueKey, &sb)
	enrichData.comments = comments

	// Linked issues
	jp.enrichLinkedIssues(issue, &sb)

	// Sprint context
	sprint := jp.enrichSprintContext(ctx, token, issueKey, &sb)
	enrichData.sprint = sprint

	// Attachments
	jp.enrichAttachments(issue, &sb)

	sb.WriteString("\n---\n")
	return sb.String(), enrichData, nil
}

// enrichmentData holds structured data from enrichment for use in trigger context
type enrichmentData struct {
	issue    *jiraIssue
	comments []jiraComment
	sprint   *jiraSprint
}

type jiraComment struct {
	Body   string `json:"body"`
	Author struct {
		DisplayName string `json:"displayName"`
	} `json:"author"`
	Created string `json:"created"`
}

type jiraSprint struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// jiraIssue represents the full JIRA issue response structure
type jiraIssue struct {
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
	} `json:"fields"`
}

// fetchFullIssue retrieves the complete issue details from JIRA
func (jp *JiraPoller) fetchFullIssue(ctx context.Context, token, issueKey string) (*jiraIssue, error) {
	fields := "summary,description,status,issuetype,priority,assignee,reporter,components,labels,issuelinks,attachment"
	url := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=%s", jp.baseURL, issueKey, fields)

	data, err := jp.jiraRequest(ctx, token, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	var issue jiraIssue
	if err := json.Unmarshal(data, &issue); err != nil {
		return nil, fmt.Errorf("parsing issue response: %w", err)
	}

	return &issue, nil
}

// enrichIssueComments fetches and formats comments for the issue
func (jp *JiraPoller) enrichIssueComments(ctx context.Context, token, issueKey string, sb *strings.Builder) []jiraComment {
	url := fmt.Sprintf("%s/rest/api/2/issue/%s/comment?maxResults=%d&orderBy=-created", jp.baseURL, issueKey, maxCommentsNum)

	data, err := jp.jiraRequest(ctx, token, "GET", url, nil)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch comments for %s: %v", issueKey, err)
		return nil
	}

	var response struct {
		Comments []jiraComment `json:"comments"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		log.Printf("jira-enrichment: error parsing comments for %s: %v", issueKey, err)
		return nil
	}

	if len(response.Comments) > 0 {
		sb.WriteString(fmt.Sprintf("\n### Comments (%d)\n\n", len(response.Comments)))
		for _, c := range response.Comments {
			comment := c.Body
			if len(comment) > maxCommentLen {
				comment = comment[:maxCommentLen] + "\n... (truncated)"
			}
			dateStr := c.Created
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}
			sb.WriteString(fmt.Sprintf("**%s** (%s):\n%s\n\n", c.Author.DisplayName, dateStr, comment))
		}
	}

	return response.Comments
}

// enrichLinkedIssues formats linked issues information
func (jp *JiraPoller) enrichLinkedIssues(issue *jiraIssue, sb *strings.Builder) {
	if len(issue.Fields.IssueLinks) == 0 {
		return
	}

	sb.WriteString(fmt.Sprintf("\n### Linked Issues (%d)\n\n", len(issue.Fields.IssueLinks)))
	for _, link := range issue.Fields.IssueLinks {
		linkType := link.Type.Name
		if link.OutwardIssue != nil {
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", linkType, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary))
		}
		if link.InwardIssue != nil {
			sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", linkType, link.InwardIssue.Key, link.InwardIssue.Fields.Summary))
		}
	}
}

// enrichSprintContext fetches sprint information using the Agile API
func (jp *JiraPoller) enrichSprintContext(ctx context.Context, token, issueKey string, sb *strings.Builder) *jiraSprint {
	url := fmt.Sprintf("%s/rest/agile/1.0/issue/%s?fields=sprint", jp.baseURL, issueKey)

	data, err := jp.jiraRequest(ctx, token, "GET", url, nil)
	if err != nil {
		log.Printf("jira-enrichment: could not fetch sprint for %s: %v", issueKey, err)
		return nil
	}

	var response struct {
		Fields struct {
			Sprint *jiraSprint `json:"sprint"`
		} `json:"fields"`
	}

	if err := json.Unmarshal(data, &response); err != nil {
		log.Printf("jira-enrichment: error parsing sprint for %s: %v", issueKey, err)
		return nil
	}

	if response.Fields.Sprint != nil {
		sb.WriteString(fmt.Sprintf("\n### Sprint\n\n"))
		sb.WriteString(fmt.Sprintf("**Name**: %s\n", response.Fields.Sprint.Name))
		sb.WriteString(fmt.Sprintf("**State**: %s\n", response.Fields.Sprint.State))
		return response.Fields.Sprint
	}

	return nil
}

// enrichAttachments formats attachment metadata
func (jp *JiraPoller) enrichAttachments(issue *jiraIssue, sb *strings.Builder) {
	if len(issue.Fields.Attachment) == 0 {
		return
	}

	sb.WriteString(fmt.Sprintf("\n### Attachments (%d)\n\n", len(issue.Fields.Attachment)))
	for _, att := range issue.Fields.Attachment {
		sizeKB := att.Size / 1024
		sb.WriteString(fmt.Sprintf("- %s (%d KB, %s)\n", att.Filename, sizeKB, att.MimeType))
	}
}

// buildJiraTriggerContext builds the expanded trigger context map for workflow template variables
func (jp *JiraPoller) buildJiraTriggerContext(enrichData *enrichmentData, enrichedMarkdown string) map[string]interface{} {
	issue := enrichData.issue
	triggerContext := map[string]interface{}{
		"issue_key":    issue.Key,
		"issue_title":  issue.Fields.Summary,
		"issue_body":   issue.Fields.Description,
		"issue_url":    fmt.Sprintf("%s/browse/%s", jp.baseURL, issue.Key),
		"issue_status": issue.Fields.Status.Name,
		"issue_labels": issue.Fields.Labels,
		"issue_type":   issue.Fields.IssueType.Name,
		"enriched_context": enrichedMarkdown,
	}

	// Add assignee if present
	if issue.Fields.Assignee != nil {
		triggerContext["issue_assignee"] = issue.Fields.Assignee.DisplayName
	} else {
		triggerContext["issue_assignee"] = ""
	}

	// Add reporter if present
	if issue.Fields.Reporter != nil {
		triggerContext["issue_reporter"] = issue.Fields.Reporter.DisplayName
	} else {
		triggerContext["issue_reporter"] = ""
	}

	// Add priority if present
	triggerContext["issue_priority"] = issue.Fields.Priority.Name

	// Format comments for template usage
	var commentTexts []string
	for _, c := range enrichData.comments {
		comment := c.Body
		if len(comment) > maxCommentLen {
			comment = comment[:maxCommentLen] + "\n... (truncated)"
		}
		commentTexts = append(commentTexts, fmt.Sprintf("%s: %s", c.Author.DisplayName, comment))
	}
	triggerContext["issue_comments"] = strings.Join(commentTexts, "\n\n")

	// Format sprint information
	sprintInfo := ""
	if enrichData.sprint != nil {
		sprintInfo = fmt.Sprintf("%s (%s)", enrichData.sprint.Name, enrichData.sprint.State)
	}
	triggerContext["issue_sprint"] = sprintInfo

	// Format linked issues
	var linkedIssueTexts []string
	for _, link := range issue.Fields.IssueLinks {
		linkType := link.Type.Name
		if link.OutwardIssue != nil {
			linkedIssueTexts = append(linkedIssueTexts, fmt.Sprintf("[%s] %s: %s", linkType, link.OutwardIssue.Key, link.OutwardIssue.Fields.Summary))
		}
		if link.InwardIssue != nil {
			linkedIssueTexts = append(linkedIssueTexts, fmt.Sprintf("[%s] %s: %s", linkType, link.InwardIssue.Key, link.InwardIssue.Fields.Summary))
		}
	}
	triggerContext["issue_linked_issues"] = strings.Join(linkedIssueTexts, "\n")

	// Format attachments
	var attachmentNames []string
	for _, att := range issue.Fields.Attachment {
		attachmentNames = append(attachmentNames, att.Filename)
	}
	triggerContext["issue_attachments"] = strings.Join(attachmentNames, ", ")

	return triggerContext
}