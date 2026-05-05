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
	"strconv"
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

		// GitHub-specific aliases.
		"create-pr":       bridgeActionCreatePR,
		"create-prs":      bridgeActionCreatePRs,
		"await-ci":        bridgeActionAwaitCI,
		"merge-pr":        bridgeActionMergePR,
		"await-release":   bridgeActionAwaitRelease,
		"update-gh-issue": bridgeActionUpdateGHIssue,

		// GitLab-specific aliases.
		"create-mr":       bridgeActionCreateMR,
		"await-pipeline":  bridgeActionAwaitPipeline,
		"merge-mr":        bridgeActionMergeMR,
		"post-note":       bridgeActionPostNote,
		"update-gl-issue": bridgeActionUpdateGLIssue,
		"create-gl-issue": bridgeActionCreateGLIssue,

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
			Name:        "await-release",
			Description: "Wait for a GitHub release to exist by tag",
			Inputs: map[string]string{
				"repo":    "string (required) - Repository in owner/repo format",
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
			Description: "Create a new issue on GitHub or GitLab. Auto-detects SCM from inputs.",
			Inputs: map[string]string{
				"repo":         "string (GitHub) - Repository in owner/repo format",
				"project":      "string (GitLab) - Project ID or URL-encoded path",
				"title":        "string (required) - Issue title",
				"body":         "string (GitHub, optional) - Issue body/description",
				"description":  "string (GitLab, optional) - Issue description",
				"labels":       "string (GitLab, optional) - Comma-separated labels",
				"assignee_ids": "[]int (GitLab, optional) - Array of GitLab user IDs to assign",
				"milestone_id": "int (GitLab, optional) - GitLab milestone ID",
			},
			Outputs: map[string]string{
				"issue_number": "int - Issue number (GitHub)",
				"issue_url":    "string - Issue URL (GitHub)",
				"issue_iid":    "int - Issue IID (GitLab)",
			},
		},
		{
			Name:        "create-gl-issue",
			Description: "Create a new issue on GitLab",
			Inputs: map[string]string{
				"project":      "string (required) - GitLab project ID or URL-encoded path",
				"title":        "string (required) - Issue title",
				"description":  "string (optional) - Issue description",
				"labels":       "string (optional) - Comma-separated labels",
				"assignee_ids": "[]int (optional) - Array of GitLab user IDs to assign",
				"milestone_id": "int (optional) - GitLab milestone ID",
			},
			Outputs: map[string]string{
				"issue_iid": "int - Issue IID",
				"issue_url": "string - Issue URL",
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
			Description: "Search issues across GitHub or JIRA (auto-detects from inputs)",
			Inputs: map[string]string{
				"repo":        "string (GitHub) - Repository in owner/repo format",
				"jql":         "string (JIRA) - JQL query string",
				"query":       "string (GitHub) - GitHub search query",
				"max_results": "int (optional) - Maximum results to return (default varies by platform)",
			},
			Outputs: map[string]string{
				"issues":             "[]object - Array of issue objects (format varies by platform)",
				"issue_keys":         "[]string - Array of issue keys (JIRA only)",
				"total":              "int - Total number of matching issues",
				"incomplete_results": "bool - Whether search results may be incomplete (GitHub only)",
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
			switch num := item.(type) {
			case int:
				result = append(result, num)
			case float64:
				result = append(result, int(num))
			case string:
				if n, err := strconv.Atoi(num); err == nil {
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

// bridgeActionUnifiedCreateIssue creates an issue and auto-detects SCM.
func bridgeActionUnifiedCreateIssue(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	scm := detectSCM(inputs)
	switch scm {
	case "gitlab":
		return bridgeActionCreateGLIssue(ctx, inputs, credStore, teamID)
	case "github":
		// Note: This will be handled by issue #561 - for now return an error
		return &BridgeActionResult{Status: "failed", Error: "GitHub issue creation not yet implemented - see issue #561"}, nil
	default:
		return &BridgeActionResult{Status: "failed", Error: "cannot detect SCM: provide 'repo' (GitHub) or 'project' (GitLab)"}, nil
	}
}

// bridgeActionSearchIssues searches issues across GitHub or JIRA based on inputs.
func bridgeActionSearchIssues(ctx context.Context, inputs map[string]interface{}, credStore *CredentialStore, teamID string) (*BridgeActionResult, error) {
	// Check for GitHub inputs
	if repo := getStringInput(inputs, "repo"); repo != "" {
		return bridgeActionSearchGHIssues(ctx, inputs, credStore, teamID)
	}

	// Check for JIRA inputs
	if jql := getStringInput(inputs, "jql"); jql != "" {
		return bridgeActionJiraSearchIssues(ctx, inputs, credStore, teamID)
	}

	return &BridgeActionResult{
		Status: "failed",
		Error:  "cannot detect search target: provide 'repo' (GitHub) or 'jql' (JIRA)",
	}, nil
}
