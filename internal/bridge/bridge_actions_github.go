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
