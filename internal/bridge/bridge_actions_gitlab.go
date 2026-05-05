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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// bridgeActionCreateMR creates a merge request on GitLab.
func bridgeActionCreateMR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project") // GitLab project ID or URL-encoded path
	sourceBranch := getStringInput(inputs, "source_branch")
	targetBranch := getStringInput(inputs, "target_branch")
	title := getStringInput(inputs, "title")
	description := getStringInput(inputs, "description")

	if project == "" || sourceBranch == "" || targetBranch == "" || title == "" {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: project, source_branch, target_branch, title"}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitLab token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	mrBody := map[string]interface{}{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
	}
	if description != "" {
		mrBody["description"] = description
	}

	bodyJSON, _ := json.Marshal(mrBody)
	// URL-encode the project path for the API URL
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests", apiHost, encodedProject)

	respBody, err := gitlabRequest(ctx, token, "POST", apiURL, bodyJSON)
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "already exists") {
			existingURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?source_branch=%s&state=opened", apiHost, encodedProject, url.QueryEscape(sourceBranch))
			existingBody, findErr := gitlabRequest(ctx, token, "GET", existingURL, nil)
			if findErr == nil {
				var existingMRs []struct {
					IID    int    `json:"iid"`
					WebURL string `json:"web_url"`
				}
				if json.Unmarshal(existingBody, &existingMRs) == nil && len(existingMRs) > 0 {
					log.Printf("bridge-action create-mr: MR already exists for branch %s: !%d", sourceBranch, existingMRs[0].IID)
					return &BridgeActionResult{
						Status: "succeeded",
						Outputs: map[string]interface{}{
							"mr_iid": existingMRs[0].IID,
							"mr_url": existingMRs[0].WebURL,
							"reused": true,
						},
					}, nil
				}
			}
		}
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("GitLab API error creating MR: %v", err)}, nil
	}

	var mrResp struct {
		IID    int    `json:"iid"`
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &mrResp); err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to parse GitLab MR response: %v", err)}, nil
	}

	log.Printf("bridge-action create-mr: created MR !%d at %s", mrResp.IID, mrResp.WebURL)
	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"mr_iid": mrResp.IID,
			"mr_url": mrResp.WebURL,
		},
	}, nil
}

// bridgeActionPostNote posts a note (comment) on a GitLab merge request.
func bridgeActionPostNote(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	mrIID := getIntInput(inputs, "mr_iid")
	body := getStringInput(inputs, "body")

	if project == "" || mrIID == 0 {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: project, mr_iid"}, nil
	}
	if body == "" {
		body = "/lgtm"
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitLab token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	noteBody := map[string]interface{}{"body": body}
	bodyJSON, _ := json.Marshal(noteBody)
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/notes", apiHost, encodedProject, mrIID)

	_, err = gitlabRequest(ctx, token, "POST", apiURL, bodyJSON)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("GitLab API error posting note: %v", err)}, nil
	}

	log.Printf("bridge-action post-note: posted note on MR !%d in %s", mrIID, project)
	return &BridgeActionResult{
		Status:  "succeeded",
		Outputs: map[string]interface{}{"posted": true},
	}, nil
}

// bridgeActionAwaitPipeline polls a GitLab MR's pipelines until the latest one reaches a terminal state.
func bridgeActionAwaitPipeline(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	mrIID := getIntInput(inputs, "mr_iid")
	timeout := getIntInput(inputs, "timeout")

	if project == "" || mrIID == 0 {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: project, mr_iid"}, nil
	}
	if timeout <= 0 {
		timeout = 900
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitLab token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollInterval := 30 * time.Second

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return &BridgeActionResult{Status: "failed", Error: "context cancelled"}, nil
		default:
		}

		apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/pipelines", apiHost, encodedProject, mrIID)
		data, err := gitlabRequest(ctx, token, "GET", apiURL, nil)
		if err != nil {
			log.Printf("bridge-action await-pipeline: error fetching pipelines: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		var pipelines []struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
			WebURL string `json:"web_url"`
		}
		if err := json.Unmarshal(data, &pipelines); err != nil {
			log.Printf("bridge-action await-pipeline: error parsing pipelines: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		if len(pipelines) == 0 {
			// No pipeline yet — check again after a bit
			if time.Since(deadline.Add(-time.Duration(timeout)*time.Second)) > 90*time.Second {
				log.Printf("bridge-action await-pipeline: no pipelines after 90s, treating as passed")
				return &BridgeActionResult{Status: "succeeded", Outputs: map[string]interface{}{"status": "passed"}}, nil
			}
			time.Sleep(pollInterval)
			continue
		}

		latest := pipelines[0] // GitLab returns most recent first
		switch latest.Status {
		case "success":
			log.Printf("bridge-action await-pipeline: pipeline %d passed for MR !%d", latest.ID, mrIID)
			return &BridgeActionResult{Status: "succeeded", Outputs: map[string]interface{}{"status": "passed", "pipeline_url": latest.WebURL}}, nil
		case "failed", "canceled":
			log.Printf("bridge-action await-pipeline: pipeline %d %s for MR !%d", latest.ID, latest.Status, mrIID)
			return &BridgeActionResult{Status: "failed", Outputs: map[string]interface{}{"status": "failed", "pipeline_url": latest.WebURL}}, nil
		}
		// Still running/pending
		time.Sleep(pollInterval)
	}

	return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("timed out after %d seconds", timeout)}, nil
}

// bridgeActionMergeMR merges a merge request on GitLab.
func bridgeActionMergeMR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	mrIID := getIntInput(inputs, "mr_iid")
	deleteBranch := true
	if v, ok := inputs["delete_branch"]; ok {
		if b, ok := v.(bool); ok {
			deleteBranch = b
		}
	}

	if project == "" || mrIID == 0 {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: project, mr_iid"}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitLab token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	mergeBody := map[string]interface{}{
		"should_remove_source_branch": deleteBranch,
	}
	bodyJSON, _ := json.Marshal(mergeBody)
	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests/%d/merge", apiHost, encodedProject, mrIID)

	respBody, err := gitlabRequest(ctx, token, "PUT", apiURL, bodyJSON)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("GitLab API error merging MR: %v", err)}, nil
	}

	var mergeResp struct {
		State          string `json:"state"`
		MergeCommitSHA string `json:"merge_commit_sha"`
	}
	json.Unmarshal(respBody, &mergeResp)

	log.Printf("bridge-action merge-mr: merged MR !%d in %s (sha: %s)", mrIID, project, mergeResp.MergeCommitSHA)
	return &BridgeActionResult{
		Status:  "succeeded",
		Outputs: map[string]interface{}{"merge_sha": mergeResp.MergeCommitSHA},
	}, nil
}

// gitlabRequest performs an authenticated HTTP request to the GitLab API.
func gitlabRequest(ctx context.Context, token, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
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

// bridgeActionUpdateGLIssue updates issue metadata on GitLab.
func bridgeActionUpdateGLIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	issue := getIntInput(inputs, "issue")
	addLabels := getStringSliceInput(inputs, "add_labels")
	removeLabels := getStringSliceInput(inputs, "remove_labels")
	addAssignees := getStringSliceInput(inputs, "add_assignees")
	removeAssignees := getStringSliceInput(inputs, "remove_assignees")
	state := getStringInput(inputs, "state")

	if project == "" || issue == 0 {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: project, issue",
		}, nil
	}

	// Validate state if provided (GitLab uses "opened" instead of "open")
	if state != "" && state != "opened" && state != "closed" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "state must be 'opened' or 'closed' for GitLab",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitLab token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	encodedProject := strings.ReplaceAll(project, "/", "%2F")
	log.Printf("bridge-action update-gl-issue: updating issue #%d in %s", issue, project)

	// First, get current issue state to merge with requested changes
	issueURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d", apiHost, encodedProject, issue)
	issueData, err := gitlabRequest(ctx, token, "GET", issueURL, nil)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to get current issue state: %v", err),
		}, nil
	}

	var currentIssue struct {
		Labels    []string `json:"labels"`
		Assignees []struct {
			Username string `json:"username"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal(issueData, &currentIssue); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse current issue data: %v", err),
		}, nil
	}

	// Build update body
	updateBody := make(map[string]interface{})

	// Handle label changes
	if len(addLabels) > 0 || len(removeLabels) > 0 {
		currentLabelsMap := make(map[string]bool)
		for _, label := range currentIssue.Labels {
			currentLabelsMap[label] = true
		}

		// Add new labels
		for _, label := range addLabels {
			currentLabelsMap[label] = true
		}

		// Remove requested labels
		for _, label := range removeLabels {
			delete(currentLabelsMap, label)
		}

		// Convert back to slice
		var finalLabels []string
		for label := range currentLabelsMap {
			finalLabels = append(finalLabels, label)
		}
		updateBody["labels"] = strings.Join(finalLabels, ",")
		log.Printf("bridge-action update-gl-issue: updating labels to %v", finalLabels)
	}

	// Handle assignee changes (GitLab requires user IDs, not usernames)
	if len(addAssignees) > 0 || len(removeAssignees) > 0 {
		currentAssigneeMap := make(map[string]bool)
		for _, assignee := range currentIssue.Assignees {
			currentAssigneeMap[assignee.Username] = true
		}

		// Add new assignees
		for _, assignee := range addAssignees {
			currentAssigneeMap[assignee] = true
		}

		// Remove requested assignees
		for _, assignee := range removeAssignees {
			delete(currentAssigneeMap, assignee)
		}

		// Convert usernames to user IDs
		var assigneeIDs []int
		for username := range currentAssigneeMap {
			userURL := fmt.Sprintf("%s/api/v4/users?username=%s", apiHost, username)
			userData, err := gitlabRequest(ctx, token, "GET", userURL, nil)
			if err != nil {
				log.Printf("bridge-action update-gl-issue: warning: failed to lookup user '%s': %v", username, err)
				continue
			}

			var users []struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal(userData, &users); err != nil || len(users) == 0 {
				log.Printf("bridge-action update-gl-issue: warning: user '%s' not found", username)
				continue
			}

			assigneeIDs = append(assigneeIDs, users[0].ID)
		}
		updateBody["assignee_ids"] = assigneeIDs
		log.Printf("bridge-action update-gl-issue: updating assignee IDs to %v", assigneeIDs)
	}

	// Handle state change
	if state != "" {
		if state == "closed" {
			updateBody["state_event"] = "close"
		} else if state == "opened" {
			updateBody["state_event"] = "reopen"
		}
		log.Printf("bridge-action update-gl-issue: updating state to '%s'", state)
	}

	// Only make the update request if there are changes to apply
	if len(updateBody) > 0 {
		bodyJSON, _ := json.Marshal(updateBody)
		_, err = gitlabRequest(ctx, token, "PUT", issueURL, bodyJSON)
		if err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to update issue: %v", err),
			}, nil
		}
		log.Printf("bridge-action update-gl-issue: successfully updated issue #%d", issue)
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"updated": true,
		},
	}, nil
}

// bridgeActionSearchGLIssues searches GitLab issues using the Issues API.
func bridgeActionSearchGLIssues(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	project := getStringInput(inputs, "project")
	search := getStringInput(inputs, "search")
	labels := getStringInput(inputs, "labels")
	state := getStringInput(inputs, "state")
	maxResults := getIntInput(inputs, "max_results")

	if project == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: project",
		}, nil
	}

	// Set defaults
	if state == "" {
		state = "opened"
	}
	if maxResults == 0 {
		maxResults = 20
	}
	if maxResults > 100 {
		maxResults = 100
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitLab token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://gitlab.cee.redhat.com"
	}

	// URL-encode the project path
	encodedProject := strings.ReplaceAll(project, "/", "%2F")

	// Build query parameters
	queryParams := url.Values{}
	queryParams.Set("state", state)
	queryParams.Set("per_page", fmt.Sprintf("%d", maxResults))

	if search != "" {
		queryParams.Set("search", search)
	}
	if labels != "" {
		queryParams.Set("labels", labels)
	}

	searchURL := fmt.Sprintf("%s/api/v4/projects/%s/issues?%s", apiHost, encodedProject, queryParams.Encode())

	respBody, err := gitlabRequest(ctx, token, "GET", searchURL, nil)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitLab API error searching issues: %v", err),
		}, nil
	}

	var issues []struct {
		IID    int      `json:"iid"`
		Title  string   `json:"title"`
		State  string   `json:"state"`
		WebURL string   `json:"web_url"`
		Labels []string `json:"labels"`
	}

	if err := json.Unmarshal(respBody, &issues); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse GitLab issues response: %v", err),
		}, nil
	}

	// Build structured output
	var issueObjects []map[string]interface{}
	for _, issue := range issues {
		issueObj := map[string]interface{}{
			"iid":     issue.IID,
			"title":   issue.Title,
			"state":   issue.State,
			"web_url": issue.WebURL,
			"labels":  issue.Labels,
		}
		issueObjects = append(issueObjects, issueObj)
	}

	log.Printf("bridge-action search-gl-issues: found %d issues in %s", len(issueObjects), project)

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"issues": issueObjects,
			"total":  len(issueObjects),
		},
	}, nil
}
