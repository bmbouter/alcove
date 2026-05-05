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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// bridgeActionJiraCreateIssue creates a new JIRA issue.
func bridgeActionJiraCreateIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	summary := getStringInput(inputs, "summary")
	issueType := getStringInput(inputs, "issue_type")
	description := getStringInput(inputs, "description")
	priority := getStringInput(inputs, "priority")

	if project == "" || summary == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: project, summary",
		}, nil
	}

	if issueType == "" {
		issueType = "Task"
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "jira", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire JIRA token: %v", err),
		}, nil
	}

	if apiHost == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "jira credential has no api_host configured — set api_host when creating the jira credential",
		}, nil
	}

	// Build request body
	reqBody := map[string]interface{}{
		"fields": map[string]interface{}{
			"project": map[string]interface{}{
				"key": project,
			},
			"summary": summary,
			"issuetype": map[string]interface{}{
				"name": issueType,
			},
		},
	}

	fields := reqBody["fields"].(map[string]interface{})

	// Add optional description (converted to ADF)
	if description != "" {
		fields["description"] = wrapTextInADF(description)
	}

	// Add optional priority
	if priority != "" {
		fields["priority"] = map[string]interface{}{
			"name": priority,
		}
	}

	// Handle labels ([]string)
	if labelsRaw, ok := inputs["labels"]; ok && labelsRaw != nil {
		switch v := labelsRaw.(type) {
		case []interface{}:
			var labels []string
			for _, label := range v {
				if str, ok := label.(string); ok {
					labels = append(labels, str)
				}
			}
			if len(labels) > 0 {
				fields["labels"] = labels
			}
		case []string:
			if len(v) > 0 {
				fields["labels"] = v
			}
		}
	}

	// Handle components ([]string)
	if componentsRaw, ok := inputs["components"]; ok && componentsRaw != nil {
		switch v := componentsRaw.(type) {
		case []interface{}:
			var components []map[string]interface{}
			for _, comp := range v {
				if str, ok := comp.(string); ok {
					components = append(components, map[string]interface{}{
						"name": str,
					})
				}
			}
			if len(components) > 0 {
				fields["components"] = components
			}
		case []string:
			var components []map[string]interface{}
			for _, comp := range v {
				components = append(components, map[string]interface{}{
					"name": comp,
				})
			}
			if len(components) > 0 {
				fields["components"] = components
			}
		}
	}

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error marshaling request: %v", err),
		}, nil
	}

	// Create issue
	createURL := fmt.Sprintf("%s/rest/api/3/issue", apiHost)
	respData, err := jiraRequest(ctx, token, "POST", createURL, reqJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error creating issue: %v", err),
		}, nil
	}

	var createResp struct {
		Key  string `json:"key"`
		Self string `json:"self"`
	}
	if err := json.Unmarshal(respData, &createResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error parsing create response: %v", err),
		}, nil
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"issue_key": createResp.Key,
			"issue_url": fmt.Sprintf("%s/browse/%s", apiHost, createResp.Key),
		},
	}, nil
}

// bridgeActionJiraTransitionIssue transitions a JIRA issue to a new status.
func bridgeActionJiraTransitionIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	issueKey := getStringInput(inputs, "issue_key")
	transition := getStringInput(inputs, "transition")

	if issueKey == "" || transition == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: issue_key, transition",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "jira", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire JIRA token: %v", err),
		}, nil
	}

	if apiHost == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "jira credential has no api_host configured — set api_host when creating the jira credential",
		}, nil
	}

	// First, get available transitions
	transitionsURL := fmt.Sprintf("%s/rest/api/3/issue/%s/transitions", apiHost, issueKey)
	respData, err := jiraRequest(ctx, token, "GET", transitionsURL, nil)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error getting transitions for %s: %v", issueKey, err),
		}, nil
	}

	var transitionsResp struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal(respData, &transitionsResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error parsing transitions response: %v", err),
		}, nil
	}

	// Find transition ID by name, or try using transition as ID directly
	var transitionID string
	for _, t := range transitionsResp.Transitions {
		if strings.EqualFold(t.Name, transition) {
			transitionID = t.ID
			break
		}
	}

	// If not found by name, try treating input as numeric ID
	if transitionID == "" {
		if _, err := strconv.Atoi(transition); err == nil {
			transitionID = transition
		} else {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("transition '%s' not found for issue %s", transition, issueKey),
			}, nil
		}
	}

	// Perform the transition
	transitionReq := map[string]interface{}{
		"transition": map[string]interface{}{
			"id": transitionID,
		},
	}

	transitionJSON, err := json.Marshal(transitionReq)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error marshaling transition request: %v", err),
		}, nil
	}

	_, err = jiraRequest(ctx, token, "POST", transitionsURL, transitionJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error transitioning issue %s: %v", issueKey, err),
		}, nil
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"transitioned": true,
		},
	}, nil
}

// bridgeActionJiraAddComment adds a comment to a JIRA issue.
func bridgeActionJiraAddComment(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	issueKey := getStringInput(inputs, "issue_key")
	body := getStringInput(inputs, "body")

	if issueKey == "" || body == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: issue_key, body",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "jira", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire JIRA token: %v", err),
		}, nil
	}

	if apiHost == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "jira credential has no api_host configured — set api_host when creating the jira credential",
		}, nil
	}

	// Build comment request with ADF body
	commentReq := map[string]interface{}{
		"body": wrapTextInADF(body),
	}

	commentJSON, err := json.Marshal(commentReq)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error marshaling comment request: %v", err),
		}, nil
	}

	// Add comment
	commentURL := fmt.Sprintf("%s/rest/api/3/issue/%s/comment", apiHost, issueKey)
	respData, err := jiraRequest(ctx, token, "POST", commentURL, commentJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error adding comment to %s: %v", issueKey, err),
		}, nil
	}

	var commentResp struct {
		ID   string `json:"id"`
		Self string `json:"self"`
	}
	if err := json.Unmarshal(respData, &commentResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error parsing comment response: %v", err),
		}, nil
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"comment_id":  commentResp.ID,
			"comment_url": fmt.Sprintf("%s/browse/%s?focusedCommentId=%s", apiHost, issueKey, commentResp.ID),
		},
	}, nil
}

// bridgeActionJiraSearchIssues searches JIRA issues using JQL.
func bridgeActionJiraSearchIssues(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	jql := getStringInput(inputs, "jql")

	if jql == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required input: jql",
		}, nil
	}

	maxResults := getIntInput(inputs, "max_results")
	if maxResults == 0 {
		maxResults = 50
	}
	if maxResults > 100 {
		maxResults = 100
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "jira", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire JIRA token: %v", err),
		}, nil
	}

	if apiHost == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "jira credential has no api_host configured — set api_host when creating the jira credential",
		}, nil
	}

	// Search issues
	searchURL := fmt.Sprintf("%s/rest/api/3/search?jql=%s&maxResults=%d&fields=key,summary,status,issuetype,priority",
		apiHost, url.QueryEscape(jql), maxResults)

	respData, err := jiraRequest(ctx, token, "GET", searchURL, nil)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error searching issues with JQL '%s': %v", jql, err),
		}, nil
	}

	var searchResp struct {
		Total  int `json:"total"`
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary   string `json:"summary"`
				Status    struct {
					Name string `json:"name"`
				} `json:"status"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
				Priority *struct {
					Name string `json:"name"`
				} `json:"priority"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(respData, &searchResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("error parsing search response: %v", err),
		}, nil
	}

	// Build structured output
	var issueKeys []string
	var issues []map[string]interface{}

	for _, issue := range searchResp.Issues {
		issueKeys = append(issueKeys, issue.Key)

		issueObj := map[string]interface{}{
			"key":     issue.Key,
			"summary": issue.Fields.Summary,
			"status":  issue.Fields.Status.Name,
			"type":    issue.Fields.IssueType.Name,
			"url":     fmt.Sprintf("%s/browse/%s", apiHost, issue.Key),
		}

		// Priority is optional in JIRA
		if issue.Fields.Priority != nil {
			issueObj["priority"] = issue.Fields.Priority.Name
		} else {
			issueObj["priority"] = ""
		}

		issues = append(issues, issueObj)
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"issues":     issues,
			"issue_keys": issueKeys,
			"total":      searchResp.Total,
		},
	}, nil
}

// jiraRequest performs an authenticated HTTP request to the JIRA API.
func jiraRequest(ctx context.Context, credential, method, reqURL string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return nil, err
	}

	// JIRA Cloud uses Basic auth for email:api_token credentials, Bearer for plain tokens
	if strings.Contains(credential, ":") {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential)))
	} else {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "alcove-bridge-action")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// wrapTextInADF converts plain text to minimal Atlassian Document Format.
func wrapTextInADF(text string) map[string]interface{} {
	if text == "" {
		return map[string]interface{}{
			"type":    "doc",
			"version": 1,
			"content": []interface{}{},
		}
	}

	return map[string]interface{}{
		"type":    "doc",
		"version": 1,
		"content": []interface{}{
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": text,
					},
				},
			},
		},
	}
}