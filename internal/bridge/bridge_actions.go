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

		// GitHub-specific aliases.
		"create-pr": bridgeActionCreatePR,
		"await-ci":  bridgeActionAwaitCI,
		"merge-pr":  bridgeActionMergePR,

		// GitLab-specific aliases.
		"create-mr":      bridgeActionCreateMR,
		"await-pipeline": bridgeActionAwaitPipeline,
		"merge-mr":       bridgeActionMergeMR,
		"post-note":      bridgeActionPostNote,
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
