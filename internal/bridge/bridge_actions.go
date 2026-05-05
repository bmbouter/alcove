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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BridgeActionResult is the result of executing a bridge action.
type BridgeActionResult struct {
	Status  string                 `json:"status"`  // "succeeded" or "failed"
	Outputs map[string]interface{} `json:"outputs"` // Action outputs
	Error   string                 `json:"error"`   // Error message if failed
}

// BridgeActionHandler is a function that executes a bridge action.
type BridgeActionHandler func(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error)

// BridgeActionSchema describes a bridge action's inputs and outputs for the API.
type BridgeActionSchema struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Inputs      map[string]string `json:"inputs"`  // name -> type description
	Outputs     map[string]string `json:"outputs"` // name -> type description
}

// RegisterBridgeActions returns a map of all built-in bridge actions.
func RegisterBridgeActions() map[string]BridgeActionHandler {
	return map[string]BridgeActionHandler{
		// Unified actions (auto-detect SCM from inputs).
		"create-merge-request": bridgeActionUnifiedCreateMR,
		"await-checks":         bridgeActionUnifiedAwaitChecks,
		"merge":                bridgeActionUnifiedMerge,
		"comment":              bridgeActionUnifiedComment,
		"update-issue":         bridgeActionUnifiedUpdateIssue,
		"create-issue":         bridgeActionUnifiedCreateIssue,
		"rebase":               bridgeActionUnifiedRebase,

		// GitHub-specific aliases.
		"create-pr":       bridgeActionCreatePR,
		"create-prs":      bridgeActionCreatePRs,
		"await-ci":        bridgeActionAwaitCI,
		"merge-pr":        bridgeActionMergePR,
		"rebase-pr":       bridgeActionRebasePR,
		"await-release":   bridgeActionUnifiedAwaitRelease,
		"update-gh-issue": bridgeActionUpdateGHIssue,
		"create-gh-issue": bridgeActionCreateGHIssue,

		// GitLab-specific aliases.
		"create-mr":         bridgeActionCreateMR,
		"await-pipeline":    bridgeActionAwaitPipeline,
		"merge-mr":          bridgeActionMergeMR,
		"post-note":         bridgeActionPostNote,
		"await-gl-release":  bridgeActionAwaitGLRelease,
		"update-gl-issue":   bridgeActionUpdateGLIssue,
		"create-gl-issue":   bridgeActionCreateGLIssue,
		"search-gl-issues": bridgeActionSearchGLIssues,

		// JIRA-specific actions.
		"jira-create-issue":     bridgeActionJiraCreateIssue,
		"jira-transition-issue": bridgeActionJiraTransitionIssue,
		"jira-add-comment":      bridgeActionJiraAddComment,
		"jira-search-issues":    bridgeActionJiraSearchIssues,

		// Search actions.
		"search-gh-issues": bridgeActionSearchGHIssues,
		"search-issues":    bridgeActionSearchIssues,
	}
}

// RegisterBridgeActionsWithDB returns a map of bridge actions with database-enabled merge serialization.
func RegisterBridgeActionsWithDB(db *pgxpool.Pool) map[string]BridgeActionHandler {
	// Create the serialized merge action with advisory lock
	serializedMergePR := createSerializedMergeAction(db, bridgeActionMergePR)
	serializedUnifiedMerge := createSerializedMergeAction(db, bridgeActionUnifiedMerge)

	actions := RegisterBridgeActions()
	// Override merge actions with serialized versions
	actions["merge-pr"] = serializedMergePR
	actions["merge"] = serializedUnifiedMerge

	return actions
}

// createSerializedMergeAction wraps a merge action with PostgreSQL advisory locking.
func createSerializedMergeAction(db *pgxpool.Pool, originalAction BridgeActionHandler) BridgeActionHandler {
	return func(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
		// Extract repo information for lock key
		repo := getStringInput(inputs, "repo")
		project := getStringInput(inputs, "project")
		base := getStringInput(inputs, "base")

		var lockKey string
		if repo != "" {
			// GitHub repo format: owner/repo
			if base == "" {
				base = "main" // default base branch
			}
			lockKey = repo + "/" + base
		} else if project != "" {
			// GitLab project format
			if base == "" {
				base = "main" // default base branch
			}
			lockKey = project + "/" + base
		} else {
			// If we can't determine repo, fallback to original action without locking
			return originalAction(ctx, inputs, credStore, teamID)
		}

		// Generate a hash-based lock ID for the repo+base combination
		hasher := sha256.New()
		hasher.Write([]byte(lockKey))
		lockHash := hex.EncodeToString(hasher.Sum(nil))

		// Convert first 8 bytes of hash to int64 for advisory lock
		var lockID int64
		for i := 0; i < 8 && i < len(lockHash)/2; i++ {
			b, _ := hex.DecodeString(lockHash[i*2 : i*2+2])
			lockID = (lockID << 8) | int64(b[0])
		}

		// Try to acquire advisory lock with timeout and retry
		maxAttempts := 5
		backoffSeconds := 10

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			// Try to acquire the lock
			var acquired bool
			err := db.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired)
			if err != nil {
				return &BridgeActionResult{
					Status: "failed",
					Error:  fmt.Sprintf("failed to acquire merge lock: %v", err),
				}, nil
			}

			if acquired {
				// Successfully acquired lock - execute the action and release lock
				result, actionErr := originalAction(ctx, inputs, credStore, teamID)

				// Always release the lock, even if the action failed
				_, releaseErr := db.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
				if releaseErr != nil {
					// Log but don't override the original action error
					fmt.Printf("warning: failed to release merge lock %d: %v\n", lockID, releaseErr)
				}

				return result, actionErr
			}

			// Lock not acquired - wait and retry if not the last attempt
			if attempt < maxAttempts {
				fmt.Printf("merge lock not available for %s (attempt %d/%d), retrying in %ds...\n", lockKey, attempt, maxAttempts, backoffSeconds)
				select {
				case <-ctx.Done():
					return &BridgeActionResult{
						Status: "failed",
						Error:  "context cancelled while waiting for merge lock",
					}, nil
				case <-time.After(time.Duration(backoffSeconds) * time.Second):
					// Continue to next attempt
				}
			}
		}

		// Failed to acquire lock after all attempts
		return &BridgeActionResult{
			Status: "failed",
			Error:  fmt.Sprintf("failed to acquire merge lock for %s after %d attempts", lockKey, maxAttempts),
		}, nil
	}
}

// ListBridgeActionSchemas returns the schemas for all bridge actions.
func ListBridgeActionSchemas() []BridgeActionSchema {
	return []BridgeActionSchema{
		{
			Name:        "create-merge-request",
			Description: "Create a pull request (GitHub) or merge request (GitLab). Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":          "string (GitHub) - Repository in owner/repo format",
				"project":       "string (GitLab) - Project ID or URL-encoded path",
				"branch":        "string (GitHub) - Source branch name",
				"source_branch": "string (GitLab) - Source branch name",
				"base":          "string (GitHub) - Target branch name",
				"target_branch": "string (GitLab) - Target branch name",
				"title":         "string (required) - MR/PR title",
				"body":          "string (GitHub, optional) - PR body/description",
				"description":   "string (GitLab, optional) - MR description",
				"draft":         "bool (GitHub, optional) - Create as draft PR",
			},
			Outputs: map[string]string{
				"pr_number": "int - Pull request number (GitHub)",
				"pr_url":    "string - Pull request URL (GitHub)",
				"mr_iid":    "int - Merge request IID (GitLab)",
				"mr_url":    "string - Merge request URL (GitLab)",
			},
		},
		{
			Name:        "await-checks",
			Description: "Wait for CI checks (GitHub) or pipeline (GitLab) to complete. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":    "string (GitHub) - Repository in owner/repo format",
				"project": "string (GitLab) - Project ID or URL-encoded path",
				"pr":      "int (GitHub) - Pull request number",
				"mr_iid":  "int (GitLab) - Merge request IID",
				"timeout": "int (optional) - Timeout in seconds (default 900)",
			},
			Outputs: map[string]string{
				"status":        "string - CI result: 'passed' or 'failed'",
				"failure_logs":  "string - Concatenated failure logs (GitHub, if failed)",
				"failed_checks": "[]string - Names of failed checks (GitHub)",
				"pipeline_url":  "string - Pipeline URL (GitLab)",
			},
		},
		{
			Name:        "merge",
			Description: "Merge a pull request (GitHub) or merge request (GitLab). Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":          "string (GitHub) - Repository in owner/repo format",
				"project":       "string (GitLab) - Project ID or URL-encoded path",
				"pr":            "int (GitHub) - Pull request number",
				"mr_iid":        "int (GitLab) - Merge request IID",
				"method":        "string (GitHub, optional) - Merge method: merge, squash, rebase (default merge)",
				"delete_branch": "bool (optional) - Delete source branch after merge (default true)",
			},
			Outputs: map[string]string{
				"merge_sha": "string - The SHA of the merge commit",
			},
		},
		{
			Name:        "comment",
			Description: "Post a comment on a pull request (GitHub) or merge request note (GitLab). Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":    "string (GitHub) - Repository in owner/repo format",
				"project": "string (GitLab) - Project ID or URL-encoded path",
				"pr":      "int (GitHub) - Pull request number",
				"mr_iid":  "int (GitLab) - Merge request IID",
				"body":    "string (required) - Comment body text",
			},
			Outputs: map[string]string{
				"posted": "bool - Whether the comment was posted",
			},
		},
		{
			Name:        "rebase",
			Description: "Update/rebase a pull request (GitHub) or merge request (GitLab) branch to latest base. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":          "string (GitHub) - Repository in owner/repo format",
				"project":       "string (GitLab) - Project ID or URL-encoded path",
				"pr":            "int (GitHub) - Pull request number",
				"mr_iid":        "int (GitLab) - Merge request IID",
				"update_method": "string (GitHub, optional) - Update method: merge, rebase (default merge)",
			},
			Outputs: map[string]string{
				"status":       "string - Update result: 'rebased', 'conflict', 'up_to_date'",
				"new_head_sha": "string - The SHA of the new HEAD after rebase (if rebased)",
				"error":        "string - Error details if status is 'conflict'",
			},
		},
		{
			Name:        "create-pr",
			Description: "Create a pull request on GitHub",
			Inputs: map[string]string{
				"repo":   "string (required) - Repository in owner/repo format",
				"branch": "string (required) - Source branch name",
				"base":   "string (required) - Target branch name",
				"title":  "string (required) - PR title",
				"body":   "string (optional) - PR body/description",
				"draft":  "bool (optional) - Create as draft PR",
			},
			Outputs: map[string]string{
				"pr_number": "int - Pull request number",
				"pr_url":    "string - Pull request URL",
			},
		},
		{
			Name:        "await-ci",
			Description: "Wait for CI checks to complete on a pull request",
			Inputs: map[string]string{
				"repo":    "string (required) - Repository in owner/repo format",
				"pr":      "int (required) - Pull request number",
				"timeout": "int (optional) - Timeout in seconds (default 900)",
			},
			Outputs: map[string]string{
				"status":        "string - CI result: 'passed' or 'failed'",
				"failure_logs":  "string - Concatenated failure logs (if failed)",
				"failed_checks": "[]string - Names of failed checks",
			},
		},
		{
			Name:        "merge-pr",
			Description: "Merge a pull request on GitHub",
			Inputs: map[string]string{
				"repo":          "string (required) - Repository in owner/repo format",
				"pr":            "int (required) - Pull request number",
				"method":        "string (optional) - Merge method: merge, squash, rebase (default merge)",
				"delete_branch": "bool (optional) - Delete source branch after merge (default true)",
			},
			Outputs: map[string]string{
				"merge_sha": "string - The SHA of the merge commit",
			},
		},
		{
			Name:        "rebase-pr",
			Description: "Update/rebase a pull request branch to latest base on GitHub",
			Inputs: map[string]string{
				"repo":          "string (required) - Repository in owner/repo format",
				"pr":            "int (required) - Pull request number",
				"update_method": "string (optional) - Update method: merge, rebase (default merge)",
			},
			Outputs: map[string]string{
				"status":       "string - Update result: 'rebased', 'conflict', 'up_to_date'",
				"new_head_sha": "string - The SHA of the new HEAD after rebase (if rebased)",
				"error":        "string - Error details if status is 'conflict'",
			},
		},
		{
			Name:        "await-release",
			Description: "Wait for a release to exist by tag. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":    "string (GitHub) - Repository in owner/repo format",
				"project": "string (GitLab) - Project ID or URL-encoded path",
				"tag":     "string (required) - Release tag (e.g. v0.35.5)",
				"timeout": "int (optional) - Timeout in seconds (default 900)",
			},
			Outputs: map[string]string{
				"release_url": "string - The HTML URL of the release",
			},
		},
		{
			Name:        "await-gl-release",
			Description: "Wait for a GitLab release to exist by tag",
			Inputs: map[string]string{
				"project": "string (required) - Project ID or URL-encoded path",
				"tag":     "string (required) - Release tag (e.g. v0.35.5)",
				"timeout": "int (optional) - Timeout in seconds (default 900)",
			},
			Outputs: map[string]string{
				"release_url": "string - The HTML URL of the release",
			},
		},
		{
			Name:        "update-issue",
			Description: "Update issue metadata (assignees, labels, state) for GitHub or GitLab. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":             "string (GitHub) - Repository in owner/repo format",
				"project":          "string (GitLab) - Project ID or URL-encoded path",
				"issue":            "int (required) - Issue number (GitHub) or Issue IID (GitLab)",
				"add_labels":       "[]string (optional) - Labels to add",
				"remove_labels":    "[]string (optional) - Labels to remove",
				"add_assignees":    "[]string (optional) - Assignees to add (usernames)",
				"remove_assignees": "[]string (optional) - Assignees to remove (usernames)",
				"state":            "string (optional) - Issue state: 'open'/'closed' (GitHub) or 'opened'/'closed' (GitLab)",
			},
			Outputs: map[string]string{
				"updated": "bool - Whether the issue was updated",
			},
		},
		{
			Name:        "create-issue",
			Description: "Create a new issue for GitHub or GitLab. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":         "string (GitHub) - Repository in owner/repo format",
				"project":      "string (GitLab) - Project ID or URL-encoded path",
				"title":        "string (required) - Issue title",
				"body":         "string (GitHub, optional) - Issue body/description",
				"description":  "string (GitLab, optional) - Issue description",
				"labels":       "[]string (GitHub, optional) OR string (GitLab, optional) - Issue labels",
				"assignees":    "[]string (GitHub, optional) - Issue assignees (usernames)",
				"assignee_ids": "[]int (GitLab, optional) - Array of user IDs to assign",
				"milestone":    "int (GitHub, optional) - Milestone number",
				"milestone_id": "int (GitLab, optional) - Milestone ID",
			},
			Outputs: map[string]string{
				"issue_number": "int - Issue number (GitHub)",
				"issue_iid":    "int - Issue IID (GitLab)",
				"issue_url":    "string - Issue HTML URL",
			},
		},
		{
			Name:        "create-gh-issue",
			Description: "Create a new issue on GitHub",
			Inputs: map[string]string{
				"repo":       "string (required) - Repository in owner/repo format",
				"title":      "string (required) - Issue title",
				"body":       "string (optional) - Issue body/description",
				"labels":     "[]string (optional) - Issue labels",
				"assignees":  "[]string (optional) - Issue assignees (usernames)",
				"milestone":  "int (optional) - Milestone number",
			},
			Outputs: map[string]string{
				"issue_number": "int - Issue number",
				"issue_url":    "string - Issue HTML URL",
			},
		},
		{
			Name:        "create-gl-issue",
			Description: "Create a new issue on GitLab",
			Inputs: map[string]string{
				"project":      "string (required) - Project ID or URL-encoded path",
				"title":        "string (required) - Issue title",
				"description":  "string (optional) - Issue description",
				"labels":       "string (optional) - Comma-separated list of labels",
				"assignee_ids": "[]int (optional) - Array of user IDs to assign",
				"milestone_id": "int (optional) - Milestone ID",
			},
			Outputs: map[string]string{
				"issue_iid": "int - Issue IID",
				"issue_url": "string - Issue web URL",
			},
		},
		{
			Name:        "jira-create-issue",
			Description: "Create a new JIRA issue",
			Inputs: map[string]string{
				"project":     "string (required) - JIRA project key",
				"summary":     "string (required) - Issue summary/title",
				"issue_type":  "string (optional) - Issue type name (default: 'Task')",
				"description": "string (optional) - Issue description (converted to ADF)",
				"priority":    "string (optional) - Issue priority name",
				"labels":      "[]string (optional) - Issue labels",
				"components":  "[]string (optional) - Component names",
			},
			Outputs: map[string]string{
				"issue_key": "string - Created issue key (e.g., 'PROJ-123')",
				"issue_url": "string - Direct link to the created issue",
			},
		},
		{
			Name:        "jira-transition-issue",
			Description: "Transition a JIRA issue to a new status",
			Inputs: map[string]string{
				"issue_key":  "string (required) - JIRA issue key (e.g., 'PROJ-123')",
				"transition": "string (required) - Transition name (e.g., 'In Progress') or numeric ID",
			},
			Outputs: map[string]string{
				"transitioned": "bool - Whether the transition succeeded",
			},
		},
		{
			Name:        "jira-add-comment",
			Description: "Add a comment to a JIRA issue",
			Inputs: map[string]string{
				"issue_key": "string (required) - JIRA issue key (e.g., 'PROJ-123')",
				"body":      "string (required) - Comment text (converted to ADF)",
			},
			Outputs: map[string]string{
				"comment_id":  "string - ID of the created comment",
				"comment_url": "string - Direct link to the comment",
			},
		},
		{
			Name:        "jira-search-issues",
			Description: "Search JIRA issues using JQL (JIRA Query Language)",
			Inputs: map[string]string{
				"jql":         "string (required) - JQL query string",
				"max_results": "int (optional) - Maximum results to return (default: 50, max: 100)",
			},
			Outputs: map[string]string{
				"issues":     "[]object - Array of issue objects with key/summary/status/type/priority/url",
				"issue_keys": "[]string - Array of issue keys for easy iteration",
				"total":      "int - Total number of matching issues",
			},
		},
		{
			Name:        "search-gh-issues",
			Description: "Search GitHub issues using GitHub search syntax",
			Inputs: map[string]string{
				"repo":        "string (required) - Repository in owner/repo format",
				"query":       "string (required) - GitHub search query (e.g. 'is:open label:bug')",
				"max_results": "int (optional) - Maximum results to return (default 20, max 100)",
			},
			Outputs: map[string]string{
				"issues":             "[]object - Array of issue objects with number/title/state/url/labels",
				"total":              "int - Total number of matching issues",
				"incomplete_results": "bool - Whether search results may be incomplete (GitHub timeout)",
			},
		},
		{
			Name:        "search-issues",
			Description: "Search issues across GitHub, GitLab, or JIRA (auto-detects from inputs)",
			Inputs: map[string]string{
				"repo":        "string (GitHub) - Repository in owner/repo format",
				"project":     "string (GitLab) - Project ID or URL-encoded path",
				"jql":         "string (JIRA) - JQL query string",
				"query":       "string (GitHub) - GitHub search query",
				"search":      "string (GitLab) - Text search within issue title and description",
				"labels":      "string (GitLab, optional) - Comma-separated list of labels to filter by",
				"state":       "string (GitLab, optional) - Issue state: 'opened', 'closed', or 'all'",
				"max_results": "int (optional) - Maximum results to return (default varies by platform)",
			},
			Outputs: map[string]string{
				"issues":             "[]object - Array of issue objects (format varies by platform)",
				"issue_keys":         "[]string - Array of issue keys (JIRA only)",
				"total":              "int - Total number of matching issues",
				"incomplete_results": "bool - Whether search results may be incomplete (GitHub only)",
			},
		},
		{
			Name:        "search-gl-issues",
			Description: "Search GitLab issues using GitLab Issues API",
			Inputs: map[string]string{
				"project":     "string (required) - GitLab project ID or URL-encoded path",
				"search":      "string (optional) - Text search within issue title and description",
				"labels":      "string (optional) - Comma-separated list of labels to filter by",
				"state":       "string (optional) - Issue state: 'opened', 'closed', or 'all' (default: opened)",
				"max_results": "int (optional) - Maximum results to return (default 20, max 100)",
			},
			Outputs: map[string]string{
				"issues": "[]object - Array of issue objects with iid/title/state/web_url/labels",
				"total":  "int - Total number of matching issues in the response",
			},
		},
	}
}

// detectSCM determines whether inputs are for GitHub or GitLab.
// Returns "github", "gitlab", or "" if ambiguous.
func detectSCM(inputs map[string]interface{}) string {
	if _, ok := inputs["project"]; ok {
		return "gitlab"
	}
	if _, ok := inputs["repo"]; ok {
		return "github"
	}
	if _, ok := inputs["mr_iid"]; ok {
		return "gitlab"
	}
	if _, ok := inputs["pr"]; ok {
		return "github"
	}
	return ""
}

// Unified bridge actions — auto-detect SCM from inputs.

func bridgeActionUnifiedCreateMR(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionCreateMR(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionCreatePR(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

func bridgeActionUnifiedAwaitChecks(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionAwaitPipeline(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionAwaitCI(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

func bridgeActionUnifiedMerge(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionMergeMR(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionMergePR(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

func bridgeActionUnifiedComment(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionPostNote(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionGitHubComment(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

// bridgeActionGitHubComment posts a comment on a GitHub pull request.
func bridgeActionGitHubComment(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	repo := getStringInput(inputs, "repo")
	pr := getIntInput(inputs, "pr")
	body := getStringInput(inputs, "body")

	if repo == "" || pr == 0 || body == "" {
		return &BridgeActionResult{Status: "failed", Error: "missing required inputs: repo, pr, body"}, nil
	}

	token, apiHost, err := credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("failed to acquire GitHub token: %v", err)}, nil
	}
	if apiHost == "" {
		apiHost = "https://api.github.com"
	}

	commentBody, _ := json.Marshal(map[string]interface{}{"body": body})
	url := fmt.Sprintf("%s/repos/%s/issues/%d/comments", apiHost, repo, pr)
	_, err = githubRequest(ctx, token, "POST", url, commentBody)
	if err != nil {
		return &BridgeActionResult{Status: "failed", Error: fmt.Sprintf("GitHub API error posting comment: %v", err)}, nil
	}

	return &BridgeActionResult{
		Status:  "succeeded",
		Outputs: map[string]interface{}{"posted": true},
	}, nil
}

// Helper functions

// getStringInput safely extracts a string input value.
func getStringInput(inputs map[string]interface{}, key string) string {
	v, ok := inputs[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// getIntInput safely extracts an integer input value.
func getIntInput(inputs map[string]interface{}, key string) int {
	v, ok := inputs[key]
	if !ok {
		return 0
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(val)
		return n
	default:
		return 0
	}
}

// getBoolInput safely extracts a boolean input value.
func getBoolInput(inputs map[string]interface{}, key string) bool {
	v, ok := inputs[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// getStringSliceInput safely extracts a string slice input value.
func getStringSliceInput(inputs map[string]interface{}, key string) []string {
	v, ok := inputs[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		var result []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

// getIntSliceInput safely extracts an integer slice input value.
func getIntSliceInput(inputs map[string]interface{}, key string) []int {
	v, ok := inputs[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []int:
		return val
	case []interface{}:
		var result []int
		for _, item := range val {
			switch i := item.(type) {
			case int:
				result = append(result, i)
			case float64:
				result = append(result, int(i))
			case string:
				if n, err := strconv.Atoi(i); err == nil {
					result = append(result, n)
				}
			}
		}
		return result
	default:
		return nil
	}
}

// bridgeActionUnifiedUpdateIssue updates issue metadata and auto-detects SCM.
func bridgeActionUnifiedUpdateIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionUpdateGLIssue(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionUpdateGHIssue(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

// bridgeActionSearchIssues searches issues across GitHub, GitLab, or JIRA based on inputs.
func bridgeActionSearchIssues(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	// Check for GitHub inputs
	if repo := getStringInput(inputs, "repo"); repo != "" {
		return bridgeActionSearchGHIssues(ctx, inputs, credStore, teamID)
	}

	// Check for GitLab inputs
	if project := getStringInput(inputs, "project"); project != "" {
		return bridgeActionSearchGLIssues(ctx, inputs, credStore, teamID)
	}

	// Check for JIRA inputs
	if jql := getStringInput(inputs, "jql"); jql != "" {
		return bridgeActionJiraSearchIssues(ctx, inputs, credStore, teamID)
	}

	return &BridgeActionResult{
		Status: "failed",
		Error:  "cannot detect search target: provide 'repo' (GitHub), 'project' (GitLab), or 'jql' (JIRA)",
	}, nil
}

// bridgeActionUnifiedRebase rebases a pull/merge request and auto-detects SCM.
func bridgeActionUnifiedRebase(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		// GitLab rebase will be implemented in Step 3
		return &BridgeActionResult{
			Status: "failed",
			Error:  "GitLab rebase is not yet implemented (see implementation plan Step 3)",
		}, nil
	case "github":
		return bridgeActionRebasePR(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

// bridgeActionUnifiedCreateIssue creates an issue and auto-detects SCM.
func bridgeActionUnifiedCreateIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionCreateGLIssue(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionCreateGHIssue(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

// bridgeActionUnifiedAwaitRelease polls for a release to exist by tag, auto-detecting GitHub or GitLab from inputs.
func bridgeActionUnifiedAwaitRelease(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionAwaitGLRelease(ctx, inputs, credStore, teamID)
	case "github":
		return bridgeActionAwaitRelease(ctx, inputs, credStore, teamID)
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}
