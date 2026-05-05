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

// bridgeActionCreatePR creates a pull request on GitHub.
func bridgeActionCreatePR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	branch := getStringInput(inputs, "branch")
	base := getStringInput(inputs, "base")
	title := getStringInput(inputs, "title")
	body := getStringInput(inputs, "body")
	draft := getBoolInput(inputs, "draft")

	if repo == "" || branch == "" || base == "" || title == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, branch, base, title",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Create the pull request.
	prBody := map[string]interface{}{
		"head":  branch,
		"base":  base,
		"title": title,
	}
	if body != "" {
		prBody["body"] = body
	}
	if draft {
		prBody["draft"] = true
	}

	bodyJSON, err := json.Marshal(prBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling PR body: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/pulls", apiHost, repo)
	respBody, err := githubRequest(ctx, token, "POST", url, bodyJSON)
	if err != nil {
		// Check if a PR already exists for this branch
		if strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "already exists") {
			// Find the existing PR
			existingURL := fmt.Sprintf("%s/repos/%s/pulls?head=%s:%s&state=open", apiHost, repo, strings.Split(repo, "/")[0], branch)
			existingBody, findErr := githubRequest(ctx, token, "GET", existingURL, nil)
			if findErr == nil {
				var existingPRs []struct {
					Number  int    `json:"number"`
					HTMLURL string `json:"html_url"`
				}
				if json.Unmarshal(existingBody, &existingPRs) == nil && len(existingPRs) > 0 {
					log.Printf("bridge-action create-pr: PR already exists for branch %s: #%d", branch, existingPRs[0].Number)
					return &BridgeActionResult{
						Status: "succeeded",
						Outputs: map[string]interface{}{
							"pr_number": existingPRs[0].Number,
							"pr_url":    existingPRs[0].HTMLURL,
							"reused":    true,
						},
					}, nil
				}
			}
		}
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub API error creating PR: %v", err),
		}, nil
	}

	var prResp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &prResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse GitHub PR response: %v", err),
		}, nil
	}

	log.Printf("bridge-action create-pr: created PR #%d at %s", prResp.Number, prResp.HTMLURL)

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"pr_number": prResp.Number,
			"pr_url":    prResp.HTMLURL,
		},
	}, nil
}

// bridgeActionAwaitCI waits for CI checks to complete on a pull request.
func bridgeActionAwaitCI(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	pr := getIntInput(inputs, "pr")
	timeout := getIntInput(inputs, "timeout")

	if repo == "" || pr == 0 {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, pr",
		}, nil
	}

	if timeout <= 0 {
		timeout = 900
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollInterval := 30 * time.Second
	startTime := time.Now()

	// Track recovery attempts
	recoveryEmptyCommitDone := false
	recoveryReopenDone := false
	var recoveryActions []string

	for time.Now().Before(deadline) {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			return &BridgeActionResult{
				Status: "failed",
				Error:  "context cancelled",
			}, nil
		default:
		}

		// Get PR to find head SHA and branch name.
		prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiHost, repo, pr)
		prData, err := githubRequest(ctx, token, "GET", prURL, nil)
		if err != nil {
			log.Printf("bridge-action await-ci: error fetching PR %s#%d: %v", repo, pr, err)
			time.Sleep(pollInterval)
			continue
		}

		var prInfo struct {
			Head struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		}
		if err := json.Unmarshal(prData, &prInfo); err != nil {
			log.Printf("bridge-action await-ci: error parsing PR response: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		if prInfo.State != "open" || prInfo.Merged {
			outputs := map[string]interface{}{
				"status":        "passed",
				"failure_logs":  "",
				"failed_checks": []string{},
			}
			if len(recoveryActions) > 0 {
				outputs["recovery_actions"] = recoveryActions
			}
			return &BridgeActionResult{
				Status:  "succeeded",
				Outputs: outputs,
			}, nil
		}

		// Check CI status.
		checksURL := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs", apiHost, repo, prInfo.Head.SHA)
		checksData, err := githubRequest(ctx, token, "GET", checksURL, nil)
		if err != nil {
			log.Printf("bridge-action await-ci: error fetching checks: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		var checks struct {
			CheckRuns []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}
		if err := json.Unmarshal(checksData, &checks); err != nil {
			log.Printf("bridge-action await-ci: error parsing checks response: %v", err)
			time.Sleep(pollInterval)
			continue
		}

		if len(checks.CheckRuns) == 0 {
			elapsed := time.Since(startTime)

			// Recovery phase 1: Push empty commit after 60s
			if elapsed > 60*time.Second && !recoveryEmptyCommitDone {
				log.Printf("bridge-action await-ci: recovery: pushing empty commit for %s#%d", repo, pr)
				err := githubPushEmptyCommit(ctx, token, apiHost, repo, prInfo.Head.Ref, prInfo.Head.SHA)
				if err != nil {
					log.Printf("bridge-action await-ci: recovery: failed to push empty commit for %s#%d: %v", repo, pr, err)
				} else {
					log.Printf("bridge-action await-ci: recovery: pushed empty commit for %s#%d", repo, pr)
					recoveryActions = append(recoveryActions, "empty_commit")
				}
				recoveryEmptyCommitDone = true
				time.Sleep(pollInterval)
				continue
			}

			// Recovery phase 2: Close and reopen PR after 120s
			if elapsed > 120*time.Second && !recoveryReopenDone {
				log.Printf("bridge-action await-ci: recovery: closing and reopening PR for %s#%d", repo, pr)
				// First close the PR
				err := githubUpdatePRState(ctx, token, apiHost, repo, pr, "closed")
				if err != nil {
					log.Printf("bridge-action await-ci: recovery: failed to close PR %s#%d: %v", repo, pr, err)
				} else {
					log.Printf("bridge-action await-ci: recovery: closed PR %s#%d", repo, pr)
					// Then reopen it
					err = githubUpdatePRState(ctx, token, apiHost, repo, pr, "open")
					if err != nil {
						log.Printf("bridge-action await-ci: recovery: failed to reopen PR %s#%d: %v", repo, pr, err)
					} else {
						log.Printf("bridge-action await-ci: recovery: reopened PR %s#%d", repo, pr)
						recoveryActions = append(recoveryActions, "close_reopen")
					}
				}
				recoveryReopenDone = true
				time.Sleep(pollInterval)
				continue
			}

			// Continue polling after recovery attempts, no longer treating as passed
			time.Sleep(pollInterval)
			continue
		}

		allComplete := true
		anyFailed := false
		var failedCheckNames []string
		var failedCheckIDs []int64

		for _, cr := range checks.CheckRuns {
			if cr.Status != "completed" {
				allComplete = false
				break
			}
			if cr.Conclusion != "success" && cr.Conclusion != "skipped" {
				anyFailed = true
				failedCheckNames = append(failedCheckNames, cr.Name)
				failedCheckIDs = append(failedCheckIDs, cr.ID)
			}
		}

		if !allComplete {
			time.Sleep(pollInterval)
			continue
		}

		if !anyFailed {
			log.Printf("bridge-action await-ci: CI passed for %s#%d", repo, pr)
			outputs := map[string]interface{}{
				"status":        "passed",
				"failure_logs":  "",
				"failed_checks": []string{},
			}
			if len(recoveryActions) > 0 {
				outputs["recovery_actions"] = recoveryActions
			}
			return &BridgeActionResult{
				Status:  "succeeded",
				Outputs: outputs,
			}, nil
		}

		// CI failed — fetch failure logs.
		log.Printf("bridge-action await-ci: CI failed for %s#%d: %v", repo, pr, failedCheckNames)
		var failureLogs strings.Builder
		for i, checkID := range failedCheckIDs {
			logURL := fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", apiHost, repo, checkID)
			logData, err := githubRequest(ctx, token, "GET", logURL, nil)
			if err != nil {
				failureLogs.WriteString(fmt.Sprintf("\n### %s\nCould not fetch logs: %v\n", failedCheckNames[i], err))
				continue
			}
			logStr := string(logData)
			if len(logStr) > 3000 {
				logStr = logStr[len(logStr)-3000:]
			}
			failureLogs.WriteString(fmt.Sprintf("\n### %s\n```\n%s\n```\n", failedCheckNames[i], logStr))
		}

		outputs := map[string]interface{}{
			"status":        "failed",
			"failure_logs":  failureLogs.String(),
			"failed_checks": failedCheckNames,
		}
		if len(recoveryActions) > 0 {
			outputs["recovery_actions"] = recoveryActions
		}

		return &BridgeActionResult{
			Status:  "failed", // CI failed, so the step should be marked as failed.
			Outputs: outputs,
		}, nil
	}

	// Timeout.
	log.Printf("bridge-action await-ci: timed out waiting for CI on %s#%d", repo, pr)
	result := &BridgeActionResult{
		Status: "failed",
		Error:  fmt.Sprintf("timed out after %d seconds waiting for CI checks", timeout),
	}
	if len(recoveryActions) > 0 {
		if result.Outputs == nil {
			result.Outputs = make(map[string]interface{})
		}
		result.Outputs["recovery_actions"] = recoveryActions
	}
	return result, nil
}

// bridgeActionMergePR merges a pull request on GitHub.
func bridgeActionMergePR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	pr := getIntInput(inputs, "pr")
	method := getStringInput(inputs, "method")
	deleteBranch := true
	if v, ok := inputs["delete_branch"]; ok {
		if b, ok := v.(bool); ok {
			deleteBranch = b
		}
	}

	if repo == "" || pr == 0 {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, pr",
		}, nil
	}

	if method == "" {
		method = "merge"
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Merge the PR.
	mergeBody := map[string]interface{}{
		"merge_method": method,
	}
	bodyJSON, err := json.Marshal(mergeBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling merge body: %w", err)
	}

	mergeURL := fmt.Sprintf("%s/repos/%s/pulls/%d/merge", apiHost, repo, pr)
	respBody, err := githubRequest(ctx, token, "PUT", mergeURL, bodyJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub API error merging PR: %v", err),
		}, nil
	}

	var mergeResp struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &mergeResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse merge response: %v", err),
		}, nil
	}

	if !mergeResp.Merged {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("merge failed: %s", mergeResp.Message),
		}, nil
	}

	log.Printf("bridge-action merge-pr: merged PR #%d in %s (sha: %s)", pr, repo, mergeResp.SHA)

	// Delete the branch if requested.
	if deleteBranch {
		// Get the PR to find the branch name.
		prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiHost, repo, pr)
		prData, err := githubRequest(ctx, token, "GET", prURL, nil)
		if err == nil {
			var prInfo struct {
				Head struct {
					Ref string `json:"ref"`
				} `json:"head"`
			}
			if json.Unmarshal(prData, &prInfo) == nil && prInfo.Head.Ref != "" {
				deleteURL := fmt.Sprintf("%s/repos/%s/git/refs/heads/%s", apiHost, repo, prInfo.Head.Ref)
				_, err := githubRequest(ctx, token, "DELETE", deleteURL, nil)
				if err != nil {
					log.Printf("bridge-action merge-pr: warning: failed to delete branch %s: %v", prInfo.Head.Ref, err)
				} else {
					log.Printf("bridge-action merge-pr: deleted branch %s", prInfo.Head.Ref)
				}
			}
		}
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"merge_sha": mergeResp.SHA,
		},
	}, nil
}

// bridgeActionAwaitRelease polls GitHub for a release to exist by tag.
func bridgeActionAwaitRelease(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	tag := getStringInput(inputs, "tag")
	timeout := getIntInput(inputs, "timeout")

	if repo == "" || tag == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, tag",
		}, nil
	}

	if timeout <= 0 {
		timeout = 900 // 15 minutes default
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	pollInterval := 30 * time.Second

	for time.Now().Before(deadline) {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			return &BridgeActionResult{
				Status: "failed",
				Error:  "context cancelled",
			}, nil
		default:
		}

		releaseURL := fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiHost, repo, tag)
		respBody, err := githubRequest(ctx, token, "GET", releaseURL, nil)
		if err != nil {
			// 404 means the release doesn't exist yet — keep polling.
			if strings.Contains(err.Error(), "HTTP 404") {
				log.Printf("bridge-action await-release: release %s not found yet for %s, polling...", tag, repo)
				time.Sleep(pollInterval)
				continue
			}
			// Other errors — log and retry.
			log.Printf("bridge-action await-release: error checking release %s for %s: %v", tag, repo, err)
			time.Sleep(pollInterval)
			continue
		}

		// Release exists — parse the response.
		var release struct {
			HTMLURL string `json:"html_url"`
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(respBody, &release); err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to parse release response: %v", err),
			}, nil
		}

		log.Printf("bridge-action await-release: release %s found for %s at %s", tag, repo, release.HTMLURL)
		return &BridgeActionResult{
			Status: "succeeded",
			Outputs: map[string]interface{}{
				"release_url": release.HTMLURL,
			},
		}, nil
	}

	// Timeout.
	log.Printf("bridge-action await-release: timed out waiting for release %s on %s", tag, repo)
	return &BridgeActionResult{
		Status: "failed",
		Error:  fmt.Sprintf("timed out after %d seconds waiting for release %s", timeout, tag),
	}, nil
}

// githubRequest performs an authenticated HTTP request to the GitHub API.
func githubRequest(ctx context.Context, token, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
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

// githubPushEmptyCommit pushes an empty commit to trigger CI using GitHub's Git Data API.
func githubPushEmptyCommit(ctx context.Context, token, apiHost, repo, branch, headSHA string) error {
	// Step 1: Get the commit to find the tree SHA
	commitURL := fmt.Sprintf("%s/repos/%s/git/commits/%s", apiHost, repo, headSHA)
	commitData, err := githubRequest(ctx, token, "GET", commitURL, nil)
	if err != nil {
		return fmt.Errorf("getting commit %s: %w", headSHA, err)
	}

	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(commitData, &commit); err != nil {
		return fmt.Errorf("parsing commit response: %w", err)
	}

	// Step 2: Create a new commit with the same tree (empty commit)
	newCommitBody := map[string]interface{}{
		"message": "Trigger CI (empty commit by Alcove)",
		"tree":    commit.Tree.SHA,
		"parents": []string{headSHA},
	}
	bodyJSON, err := json.Marshal(newCommitBody)
	if err != nil {
		return fmt.Errorf("marshaling new commit body: %w", err)
	}

	createCommitURL := fmt.Sprintf("%s/repos/%s/git/commits", apiHost, repo)
	newCommitData, err := githubRequest(ctx, token, "POST", createCommitURL, bodyJSON)
	if err != nil {
		return fmt.Errorf("creating new commit: %w", err)
	}

	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(newCommitData, &newCommit); err != nil {
		return fmt.Errorf("parsing new commit response: %w", err)
	}

	// Step 3: Update the branch reference
	updateRefBody := map[string]interface{}{
		"sha": newCommit.SHA,
	}
	refBodyJSON, err := json.Marshal(updateRefBody)
	if err != nil {
		return fmt.Errorf("marshaling ref update body: %w", err)
	}

	refURL := fmt.Sprintf("%s/repos/%s/git/refs/heads/%s", apiHost, repo, branch)
	_, err = githubRequest(ctx, token, "PATCH", refURL, refBodyJSON)
	if err != nil {
		// Check for 409 conflict and handle gracefully
		if strings.Contains(err.Error(), "409") {
			return fmt.Errorf("branch was updated by another process (409 conflict): %w", err)
		}
		return fmt.Errorf("updating branch ref: %w", err)
	}

	return nil
}

// githubUpdatePRState updates the state of a pull request (open/closed).
func githubUpdatePRState(ctx context.Context, token, apiHost, repo string, pr int, state string) error {
	if state != "open" && state != "closed" {
		return fmt.Errorf("invalid state '%s', must be 'open' or 'closed'", state)
	}

	updateBody := map[string]interface{}{
		"state": state,
	}
	bodyJSON, err := json.Marshal(updateBody)
	if err != nil {
		return fmt.Errorf("marshaling PR state update body: %w", err)
	}

	prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiHost, repo, pr)
	_, err = githubRequest(ctx, token, "PATCH", prURL, bodyJSON)
	if err != nil {
		return fmt.Errorf("updating PR state to %s: %w", state, err)
	}

	return nil
}

// bridgeActionUpdateGHIssue updates issue metadata on GitHub.
func bridgeActionUpdateGHIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	issue := getIntInput(inputs, "issue")
	addLabels := getStringSliceInput(inputs, "add_labels")
	removeLabels := getStringSliceInput(inputs, "remove_labels")
	addAssignees := getStringSliceInput(inputs, "add_assignees")
	removeAssignees := getStringSliceInput(inputs, "remove_assignees")
	state := getStringInput(inputs, "state")

	if repo == "" || issue == 0 {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, issue",
		}, nil
	}

	// Validate state if provided
	if state != "" && state != "open" && state != "closed" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "state must be 'open' or 'closed'",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	log.Printf("bridge-action update-gh-issue: updating issue #%d in %s", issue, repo)

	// Add labels if requested
	if len(addLabels) > 0 {
		labelsBody := map[string]interface{}{"labels": addLabels}
		bodyJSON, _ := json.Marshal(labelsBody)
		url := fmt.Sprintf("%s/repos/%s/issues/%d/labels", apiHost, repo, issue)
		_, err := githubRequest(ctx, token, "POST", url, bodyJSON)
		if err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to add labels: %v", err),
			}, nil
		}
		log.Printf("bridge-action update-gh-issue: added labels %v to issue #%d", addLabels, issue)
	}

	// Remove labels if requested
	for _, label := range removeLabels {
		url := fmt.Sprintf("%s/repos/%s/issues/%d/labels/%s", apiHost, repo, issue, label)
		_, err := githubRequest(ctx, token, "DELETE", url, nil)
		if err != nil {
			// Warn but don't fail if label doesn't exist (404)
			if strings.Contains(err.Error(), "HTTP 404") {
				log.Printf("bridge-action update-gh-issue: warning: label '%s' not found on issue #%d, skipping", label, issue)
			} else {
				return &BridgeActionResult{
					Status: "failed",
					Error:  fmt.Sprintf("failed to remove label '%s': %v", label, err),
				}, nil
			}
		} else {
			log.Printf("bridge-action update-gh-issue: removed label '%s' from issue #%d", label, issue)
		}
	}

	// Add assignees if requested
	if len(addAssignees) > 0 {
		assigneesBody := map[string]interface{}{"assignees": addAssignees}
		bodyJSON, _ := json.Marshal(assigneesBody)
		url := fmt.Sprintf("%s/repos/%s/issues/%d/assignees", apiHost, repo, issue)
		_, err := githubRequest(ctx, token, "POST", url, bodyJSON)
		if err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to add assignees: %v", err),
			}, nil
		}
		log.Printf("bridge-action update-gh-issue: added assignees %v to issue #%d", addAssignees, issue)
	}

	// Remove assignees if requested
	if len(removeAssignees) > 0 {
		assigneesBody := map[string]interface{}{"assignees": removeAssignees}
		bodyJSON, _ := json.Marshal(assigneesBody)
		url := fmt.Sprintf("%s/repos/%s/issues/%d/assignees", apiHost, repo, issue)
		_, err := githubRequest(ctx, token, "DELETE", url, bodyJSON)
		if err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to remove assignees: %v", err),
			}, nil
		}
		log.Printf("bridge-action update-gh-issue: removed assignees %v from issue #%d", removeAssignees, issue)
	}

	// Update state if requested
	if state != "" {
		stateBody := map[string]interface{}{"state": state}
		bodyJSON, _ := json.Marshal(stateBody)
		url := fmt.Sprintf("%s/repos/%s/issues/%d", apiHost, repo, issue)
		_, err := githubRequest(ctx, token, "PATCH", url, bodyJSON)
		if err != nil {
			return &BridgeActionResult{
				Status: "failed",
				Error:  fmt.Sprintf("failed to update state: %v", err),
			}, nil
		}
		log.Printf("bridge-action update-gh-issue: updated state to '%s' for issue #%d", state, issue)
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"updated": true,
		},
	}, nil
}

// bridgeActionCreateGHIssue creates a new issue on GitHub.
func bridgeActionCreateGHIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	title := getStringInput(inputs, "title")
	body := getStringInput(inputs, "body")
	labels := getStringSliceInput(inputs, "labels")
	assignees := getStringSliceInput(inputs, "assignees")
	milestone := getIntInput(inputs, "milestone")

	if repo == "" || title == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, title",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Create the issue request body.
	issueBody := map[string]interface{}{
		"title": title,
	}
	if body != "" {
		issueBody["body"] = body
	}
	if len(labels) > 0 {
		issueBody["labels"] = labels
	}
	if len(assignees) > 0 {
		issueBody["assignees"] = assignees
	}
	if milestone > 0 {
		issueBody["milestone"] = milestone
	}

	bodyJSON, err := json.Marshal(issueBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling issue body: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/issues", apiHost, repo)
	respBody, err := githubRequest(ctx, token, "POST", url, bodyJSON)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub API error creating issue: %v", err),
		}, nil
	}

	var issueResp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &issueResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse GitHub issue response: %v", err),
		}, nil
	}

	log.Printf("bridge-action create-gh-issue: created issue #%d at %s", issueResp.Number, issueResp.HTMLURL)

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"issue_number": issueResp.Number,
			"issue_url":    issueResp.HTMLURL,
		},
	}, nil
}

// bridgeActionSearchGHIssues searches GitHub issues using the Search API.
func bridgeActionSearchGHIssues(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	query := getStringInput(inputs, "query")
	maxResults := getIntInput(inputs, "max_results")

	if repo == "" || query == "" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, query",
		}, nil
	}

	if maxResults == 0 {
		maxResults = 20
	}
	if maxResults > 100 {
		maxResults = 100
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Build search URL with repo qualifier prepended
	searchURL := fmt.Sprintf("%s/search/issues?q=repo:%s+%s&per_page=%d",
		apiHost, repo, url.QueryEscape(query), maxResults)

	respBody, err := githubRequest(ctx, token, "GET", searchURL, nil)
	if err != nil {
		// Log rate limit errors clearly for debugging
		if strings.Contains(err.Error(), "HTTP 403") {
			log.Printf("bridge-action search-gh-issues: GitHub search API rate limit may be reached for %s", repo)
		}
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub Search API error: %v", err),
		}, nil
	}

	var searchResp struct {
		TotalCount        int  `json:"total_count"`
		IncompleteResults bool `json:"incomplete_results"`
		Items             []struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
			Labels  []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"items"`
	}

	if err := json.Unmarshal(respBody, &searchResp); err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to parse GitHub search response: %v", err),
		}, nil
	}

	// Build structured output
	var issues []map[string]interface{}
	for _, item := range searchResp.Items {
		// Flatten labels to string array for ergonomics
		var labels []string
		for _, label := range item.Labels {
			labels = append(labels, label.Name)
		}

		issue := map[string]interface{}{
			"number": item.Number,
			"title":  item.Title,
			"state":  item.State,
			"url":    item.HTMLURL,
			"labels": labels,
		}
		issues = append(issues, issue)
	}

	log.Printf("bridge-action search-gh-issues: found %d issues in %s (total: %d)", len(issues), repo, searchResp.TotalCount)

	outputs := map[string]interface{}{
		"issues": issues,
		"total":  searchResp.TotalCount,
	}

	// Include incomplete_results in outputs for caller awareness
	if searchResp.IncompleteResults {
		outputs["incomplete_results"] = true
	}

	return &BridgeActionResult{
		Status:  "succeeded",
		Outputs: outputs,
	}, nil
}

// bridgeActionRebasePR rebases a pull request on GitHub using the update-branch API.
func bridgeActionRebasePR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	pr := getIntInput(inputs, "pr")
	updateMethod := getStringInput(inputs, "update_method")

	if repo == "" || pr == 0 {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "missing required inputs: repo, pr",
		}, nil
	}

	if updateMethod == "" {
		updateMethod = "merge" // Default to merge method (safer than rebase)
	}
	if updateMethod != "merge" && updateMethod != "rebase" {
		return &BridgeActionResult{
			Status: "failed",
			Error:  "update_method must be 'merge' or 'rebase'",
		}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire GitHub token: %v", err),
		}, nil
	}

	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	// Call GitHub's update-branch API
	updateBody := map[string]interface{}{
		"update_method": updateMethod,
	}
	bodyJSON, err := json.Marshal(updateBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling update body: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/pulls/%d/update-branch", apiHost, repo, pr)
	respBody, err := githubRequest(ctx, token, "PUT", url, bodyJSON)
	if err != nil {
		// Check for specific error conditions
		if strings.Contains(err.Error(), "422") {
			// 422 typically indicates merge conflicts or other update issues
			log.Printf("bridge-action rebase: conflicts detected for PR #%d in %s", pr, repo)
			return &BridgeActionResult{
				Status: "failed",
				Outputs: map[string]interface{}{
					"status": "conflict",
					"error":  "Merge conflicts detected — requires manual resolution",
				},
				Error: "Merge conflicts detected during rebase",
			}, nil
		}
		// Check for up-to-date condition
		if strings.Contains(err.Error(), "Branch is up to date") || strings.Contains(strings.ToLower(err.Error()), "already up to date") {
			log.Printf("bridge-action rebase: PR #%d in %s is already up to date", pr, repo)
			return &BridgeActionResult{
				Status: "succeeded",
				Outputs: map[string]interface{}{
					"status": "up_to_date",
				},
			}, nil
		}
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("GitHub API error updating branch: %v", err),
		}, nil
	}

	var updateResp struct {
		Message string `json:"message"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(respBody, &updateResp); err != nil {
		// Even if parsing fails, the update likely succeeded if we got here
		log.Printf("bridge-action rebase: branch updated for PR #%d in %s (could not parse response)", pr, repo)
	} else {
		log.Printf("bridge-action rebase: %s for PR #%d in %s", updateResp.Message, pr, repo)
	}

	// Get the updated PR to find the new head SHA
	prURL := fmt.Sprintf("%s/repos/%s/pulls/%d", apiHost, repo, pr)
	prData, err := githubRequest(ctx, token, "GET", prURL, nil)
	var newHeadSHA string
	if err == nil {
		var prInfo struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if json.Unmarshal(prData, &prInfo) == nil {
			newHeadSHA = prInfo.Head.SHA
		}
	}

	outputs := map[string]interface{}{
		"status": "rebased",
	}
	if newHeadSHA != "" {
		outputs["new_head_sha"] = newHeadSHA
	}

	return &BridgeActionResult{
		Status:  "succeeded",
		Outputs: outputs,
	}, nil
}

// bridgeActionCreatePRs creates pull requests across multiple repos.
func bridgeActionCreatePRs(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	branch := getStringInput(inputs, "branch")
	base := getStringInput(inputs, "base")
	title := getStringInput(inputs, "title")
	body := getStringInput(inputs, "body")
	draft := getBoolInput(inputs, "draft")

	if base == "" {
		base = "main"
	}
	if branch == "" || title == "" {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: branch, title"}, nil
	}

	var repos []string
	if r, ok := inputs["repos"]; ok {
		switch v := r.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					repos = append(repos, s)
				}
			}
		case []string:
			repos = v
		}
	}
	if len(repos) == 0 {
		return &BridgeActionResult{Status: "failed", Error: "missing required input: repos (array of owner/repo strings)"}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitHub token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	var prNumbers []int
	var prURLs []string
	var succeededRepos []string
	var failedRepos []string

	for _, repo := range repos {
		// Check if branch exists in this repo.
		branchURL := fmt.Sprintf("%s/repos/%s/branches/%s", apiHost, repo, branch)
		_, err := githubRequest(ctx, token, "GET", branchURL, nil)
		if err != nil {
			log.Printf("bridge-action create-prs: branch %s not found in %s, skipping", branch, repo)
			continue
		}

		prBody := map[string]interface{}{
			"head":  branch,
			"base":  base,
			"title": title,
		}
		if body != "" {
			prBody["body"] = body
		}
		if draft {
			prBody["draft"] = true
		}

		bodyJSON, _ := json.Marshal(prBody)
		url := fmt.Sprintf("%s/repos/%s/pulls", apiHost, repo)
		respBody, err := githubRequest(ctx, token, "POST", url, bodyJSON)
		if err != nil {
			if strings.Contains(err.Error(), "422") || strings.Contains(err.Error(), "already exists") {
				existingURL := fmt.Sprintf("%s/repos/%s/pulls?head=%s:%s&state=open", apiHost, repo, strings.Split(repo, "/")[0], branch)
				existingBody, findErr := githubRequest(ctx, token, "GET", existingURL, nil)
				if findErr == nil {
					var existing []struct {
						Number  int    `json:"number"`
						HTMLURL string `json:"html_url"`
					}
					if json.Unmarshal(existingBody, &existing) == nil && len(existing) > 0 {
						prNumbers = append(prNumbers, existing[0].Number)
						prURLs = append(prURLs, existing[0].HTMLURL)
						succeededRepos = append(succeededRepos, repo)
						continue
					}
				}
			}
			log.Printf("bridge-action create-prs: failed to create PR in %s: %v", repo, err)
			failedRepos = append(failedRepos, repo)
			continue
		}

		var prResp struct {
			Number  int    `json:"number"`
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(respBody, &prResp); err != nil {
			failedRepos = append(failedRepos, repo)
			continue
		}

		log.Printf("bridge-action create-prs: created PR #%d in %s at %s", prResp.Number, repo, prResp.HTMLURL)
		prNumbers = append(prNumbers, prResp.Number)
		prURLs = append(prURLs, prResp.HTMLURL)
		succeededRepos = append(succeededRepos, repo)
	}

	return &BridgeActionResult{
		Status: "succeeded",
		Outputs: map[string]interface{}{
			"pr_numbers":   prNumbers,
			"pr_urls":      prURLs,
			"repos":        succeededRepos,
			"failed_repos": failedRepos,
		},
	}, nil
}
