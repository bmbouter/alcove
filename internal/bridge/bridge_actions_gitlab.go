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
			existingURL := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?source_branch=%s&state=opened", apiHost, encodedProject, sourceBranch)
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
		Status: "succeeded",
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
			return &BridgeActionResult{Status: "succeeded", Outputs: map[string]interface{}{"status": "failed", "pipeline_url": latest.WebURL}}, nil
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
		Status: "succeeded",
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
