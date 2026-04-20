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

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// workflowInfo represents a workflow from the API.
type workflowInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceRepo string `json:"source_repo"`
	SourceFile string `json:"source_file"`
	SyncError  string `json:"sync_error,omitempty"`
	LastSynced string `json:"last_synced"`
	TeamID     string `json:"team_id"`
}

// workflowsListResponse is the response from GET /api/v1/workflows.
type workflowsListResponse struct {
	Workflows []workflowInfo `json:"workflows"`
	Count     int            `json:"count"`
}

// workflowRunInfo represents a workflow run from the API.
type workflowRunInfo struct {
	ID          string `json:"id"`
	WorkflowID  string `json:"workflow_id"`
	Status      string `json:"status"`
	TriggerType string `json:"trigger_type,omitempty"`
	TriggerRef  string `json:"trigger_ref,omitempty"`
	CurrentStep string `json:"current_step,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	TeamID      string `json:"team_id"`
	CreatedAt   string `json:"created_at"`
}

// workflowRunsListResponse is the response from GET /api/v1/workflow-runs.
type workflowRunsListResponse struct {
	WorkflowRuns []workflowRunInfo `json:"workflow_runs"`
	Count        int               `json:"count"`
}

func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Manage workflows and workflow runs",
	}
	cmd.AddCommand(
		newWorkflowsListCmd(),
		newWorkflowsRunCmd(),
		newWorkflowsRunsCmd(),
	)
	return cmd
}

// ---------- workflows list ----------

func newWorkflowsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all workflows",
		RunE:  runWorkflowsList,
	}
}

func runWorkflowsList(cmd *cobra.Command, _ []string) error {
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/workflows", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result workflowsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.Workflows) == 0 {
		fmt.Fprintln(os.Stderr, "No workflows found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSOURCE\tLAST SYNCED\tERROR")
	for _, wf := range result.Workflows {
		lastSynced := wf.LastSynced
		if t, err := time.Parse(time.RFC3339Nano, wf.LastSynced); err == nil {
			lastSynced = t.Local().Format("2006-01-02 15:04")
		}
		syncError := wf.SyncError
		if len(syncError) > 30 {
			syncError = syncError[:27] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			wf.ID, wf.Name, wf.SourceRepo, lastSynced, syncError)
	}
	return w.Flush()
}

// ---------- workflows run ----------

func newWorkflowsRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <id-or-name>",
		Short: "Trigger a workflow run",
		Long:  "Start a new workflow run by workflow ID or name. If a name is provided, it will be resolved to a workflow ID.",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorkflowsRun,
	}
	cmd.Flags().String("trigger-ref", "", "Optional trigger reference (e.g., branch name, PR number)")
	return cmd
}

func runWorkflowsRun(cmd *cobra.Command, args []string) error {
	idOrName := args[0]
	triggerRef, _ := cmd.Flags().GetString("trigger-ref")

	// Resolve name to ID if needed: first try to list workflows and match by name.
	workflowID, err := resolveWorkflowID(cmd, idOrName)
	if err != nil {
		return err
	}

	reqBody := map[string]string{
		"workflow_id": workflowID,
	}
	if triggerRef != "" {
		reqBody["trigger_ref"] = triggerRef
	}

	resp, err := apiRequest(cmd, http.MethodPost, "/api/v1/workflow-runs", reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result workflowRunInfo
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Workflow run started: %s\n", result.ID)
	fmt.Println(result.ID)
	return nil
}

// resolveWorkflowID resolves a workflow ID or name to an ID.
// If the argument looks like a UUID (contains hyphens), it is used directly.
// Otherwise, workflows are listed and matched by name.
func resolveWorkflowID(cmd *cobra.Command, idOrName string) (string, error) {
	// If it looks like a UUID, use it directly.
	if looksLikeUUID(idOrName) {
		return idOrName, nil
	}

	// Otherwise, list workflows and match by name.
	resp, err := apiRequest(cmd, http.MethodGet, "/api/v1/workflows", nil)
	if err != nil {
		return "", fmt.Errorf("listing workflows: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("listing workflows: bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result workflowsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding workflows: %w", err)
	}

	for _, wf := range result.Workflows {
		if wf.Name == idOrName {
			return wf.ID, nil
		}
	}

	return "", fmt.Errorf("workflow %q not found", idOrName)
}

// looksLikeUUID returns true if the string contains hyphens, suggesting it is a UUID.
func looksLikeUUID(s string) bool {
	hyphenCount := 0
	for _, c := range s {
		if c == '-' {
			hyphenCount++
		}
	}
	return hyphenCount >= 4 && len(s) >= 32
}

// ---------- workflows runs ----------

func newWorkflowsRunsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List workflow runs",
		RunE:  runWorkflowsRuns,
	}
	cmd.Flags().String("status", "", "Filter by status (pending, running, completed, failed, cancelled, awaiting_approval)")
	return cmd
}

func runWorkflowsRuns(cmd *cobra.Command, _ []string) error {
	path := "/api/v1/workflow-runs"
	if status, _ := cmd.Flags().GetString("status"); status != "" {
		path += "?status=" + status
	}

	resp, err := apiRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result workflowRunsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	if len(result.WorkflowRuns) == 0 {
		fmt.Fprintln(os.Stderr, "No workflow runs found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tWORKFLOW\tSTATUS\tTRIGGER\tCURRENT STEP\tCREATED")
	for _, run := range result.WorkflowRuns {
		created := run.CreatedAt
		if t, err := time.Parse(time.RFC3339Nano, run.CreatedAt); err == nil {
			created = t.Local().Format("2006-01-02 15:04")
		}
		trigger := run.TriggerType
		if run.TriggerRef != "" {
			trigger += ":" + run.TriggerRef
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			run.ID, run.WorkflowID, run.Status, trigger, run.CurrentStep, created)
	}
	return w.Flush()
}
