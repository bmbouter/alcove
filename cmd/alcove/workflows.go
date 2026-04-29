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
	"strings"
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
	WorkflowRuns []workflowRunInfo      `json:"workflow_runs"`
	Count        int                    `json:"count"`
	Total        int                    `json:"total"`
	Summary      *workflowRunsSummary   `json:"summary,omitempty"`
}

// workflowRunsSummary contains status counts for workflow runs.
type workflowRunsSummary struct {
	Running          int `json:"running"`
	Pending          int `json:"pending"`
	Completed        int `json:"completed"`
	Failed           int `json:"failed"`
	Cancelled        int `json:"cancelled"`
	AwaitingApproval int `json:"awaiting_approval"`
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
		newWorkflowsCancelCmd(),
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
		Short: "List workflow runs with pagination and filtering",
		Long: `List workflow runs with advanced filtering options.

Examples:
  alcove workflows runs                           # List recent runs (default 25)
  alcove workflows runs --limit 50 --offset 25   # Pagination
  alcove workflows runs --status failed          # Failed runs only
  alcove workflows runs --workflow "SDLC Pipeline" # Filter by workflow name
  alcove workflows runs --since 7d               # Last 7 days
  alcove workflows runs --search "owner/repo#42" # Search by trigger ref
  alcove workflows runs --summary                # Include status summary`,
		RunE: runWorkflowsRuns,
	}
	cmd.Flags().String("status", "", "Filter by status (pending, running, completed, failed, cancelled, awaiting_approval)")
	cmd.Flags().Int("limit", 0, "Number of results per page (default 25, max 200)")
	cmd.Flags().Int("offset", 0, "Number of results to skip (default 0)")
	cmd.Flags().String("workflow", "", "Filter by workflow name (partial match)")
	cmd.Flags().String("since", "", "Filter by date: 1d, 7d, 30d, or YYYY-MM-DD")
	cmd.Flags().String("search", "", "Search by trigger ref (exact match)")
	cmd.Flags().Bool("summary", false, "Include status summary")
	return cmd
}

func runWorkflowsRuns(cmd *cobra.Command, _ []string) error {
	// Build query parameters
	params := make([]string, 0)

	if status, _ := cmd.Flags().GetString("status"); status != "" {
		params = append(params, "status="+status)
	}
	if limit, _ := cmd.Flags().GetInt("limit"); limit > 0 {
		params = append(params, fmt.Sprintf("limit=%d", limit))
	}
	if offset, _ := cmd.Flags().GetInt("offset"); offset > 0 {
		params = append(params, fmt.Sprintf("offset=%d", offset))
	}
	if workflow, _ := cmd.Flags().GetString("workflow"); workflow != "" {
		params = append(params, "workflow="+workflow)
	}
	if since, _ := cmd.Flags().GetString("since"); since != "" {
		params = append(params, "since="+since)
	}
	if search, _ := cmd.Flags().GetString("search"); search != "" {
		params = append(params, "search="+search)
	}
	if summary, _ := cmd.Flags().GetBool("summary"); summary {
		params = append(params, "summary=true")
	}

	path := "/api/v1/workflow-runs"
	if len(params) > 0 {
		path += "?" + strings.Join(params, "&")
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

	// Display summary if available
	if result.Summary != nil {
		total := result.Summary.Running + result.Summary.Pending + result.Summary.Completed +
			result.Summary.Failed + result.Summary.Cancelled + result.Summary.AwaitingApproval

		fmt.Printf("Status Summary: %d running · %d pending · %d completed · %d failed",
			result.Summary.Running, result.Summary.Pending, result.Summary.Completed, result.Summary.Failed)
		if result.Summary.Cancelled > 0 {
			fmt.Printf(" · %d cancelled", result.Summary.Cancelled)
		}
		if result.Summary.AwaitingApproval > 0 {
			fmt.Printf(" · %d awaiting approval", result.Summary.AwaitingApproval)
		}
		fmt.Printf(" (total: %d)\n\n", total)
	}

	if len(result.WorkflowRuns) == 0 {
		fmt.Fprintln(os.Stderr, "No workflow runs found.")
		return nil
	}

	// Display pagination info
	if result.Total > 0 {
		start := 1
		if offset, _ := cmd.Flags().GetInt("offset"); offset > 0 {
			start = offset + 1
		}
		end := start + result.Count - 1
		fmt.Printf("Showing %d-%d of %d workflow runs\n", start, end, result.Total)
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

// ---------- workflows cancel ----------

func newWorkflowsCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Cancel a workflow run",
		Long:  "Cancel a workflow run and all its pending/running steps. Only workflow runs in pending, running, or awaiting_approval status can be cancelled.",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorkflowsCancel,
	}
}

func runWorkflowsCancel(cmd *cobra.Command, args []string) error {
	runID := args[0]

	// Use DELETE method to cancel the workflow run
	resp, err := apiRequest(cmd, http.MethodDelete, "/api/v1/workflow-runs/"+runID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bridge returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if isJSONOutput(cmd) {
		return outputJSON(result)
	}

	fmt.Fprintf(os.Stderr, "Workflow run %s has been cancelled\n", runID)
	return nil
}
