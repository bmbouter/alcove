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

		// Get PR to find head SHA.
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
			return &BridgeActionResult{
				Status: "succeeded",
				Outputs: map[string]interface{}{
					"status":        "passed",
					"failure_logs":  "",
					"failed_checks": []string{},
				},
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
			// If no checks after 90s, treat as passed (repo has no CI configured).
			if time.Since(deadline.Add(-time.Duration(timeout)*time.Second)) > 90*time.Second {
				log.Printf("bridge-action await-ci: no check runs found after 90s for %s#%d, treating as passed", repo, pr)
				return &BridgeActionResult{
					Status: "succeeded",
					Outputs: map[string]interface{}{
						"status":        "passed",
						"failure_logs":  "",
						"failed_checks": []string{},
					},
				}, nil
			}
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
			return &BridgeActionResult{
				Status: "succeeded",
				Outputs: map[string]interface{}{
					"status":        "passed",
					"failure_logs":  "",
					"failed_checks": []string{},
				},
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

		return &BridgeActionResult{
			Status: "succeeded", // The action itself succeeded; the CI status is in the outputs.
			Outputs: map[string]interface{}{
				"status":        "failed",
				"failure_logs":  failureLogs.String(),
				"failed_checks": failedCheckNames,
			},
		}, nil
	}

	// Timeout.
	log.Printf("bridge-action await-ci: timed out waiting for CI on %s#%d", repo, pr)
	return &BridgeActionResult{
		Status: "failed",
		Error:  fmt.Sprintf("timed out after %d seconds waiting for CI checks", timeout),
	}, nil
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
