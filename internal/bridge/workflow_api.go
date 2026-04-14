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
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// workflowRunDetailResponse contains the full DAG with per-step status.
type workflowRunDetailResponse struct {
	ID          string                  `json:"id"`
	Workflow    string                  `json:"workflow"`
	Status      string                  `json:"status"`
	Steps       []workflowRunStepDetail `json:"steps"`
	StartedAt   *time.Time              `json:"started_at,omitempty"`
	FinishedAt  *time.Time              `json:"finished_at,omitempty"`
	TriggerType string                  `json:"trigger_type,omitempty"`
	TriggerRef  string                  `json:"trigger_ref,omitempty"`
}

// workflowRunStepDetail contains step information with token/cost tracking.
type workflowRunStepDetail struct {
	ID              string                 `json:"id"`
	Status          string                 `json:"status"`
	SessionID       string                 `json:"session_id,omitempty"`
	Outputs         map[string]interface{} `json:"outputs,omitempty"`
	StartedAt       *time.Time             `json:"started_at,omitempty"`
	FinishedAt      *time.Time             `json:"finished_at,omitempty"`
	Approval        string                 `json:"approval,omitempty"`
	TokensIn        int64                  `json:"tokens_in,omitempty"`
	TokensOut       int64                  `json:"tokens_out,omitempty"`
	DurationSeconds int64                  `json:"duration_seconds,omitempty"`
}

// workflowRunListItem represents a single workflow run in a list response.
type workflowRunListItem struct {
	ID          string     `json:"id"`
	WorkflowID  string     `json:"workflow_id"`
	Workflow    string     `json:"workflow"`
	Status      string     `json:"status"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	TriggerType string     `json:"trigger_type,omitempty"`
	TriggerRef  string     `json:"trigger_ref,omitempty"`
	Owner       string     `json:"owner"`
}

// workflowDefinitionResponse represents a workflow definition.
type workflowDefinitionResponse struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	SourceRepo string                   `json:"source_repo,omitempty"`
	SourceFile string                   `json:"source_file,omitempty"`
	Workflow   []map[string]interface{} `json:"workflow,omitempty"`
	Owner      string                   `json:"owner"`
	LastSynced *time.Time               `json:"last_synced,omitempty"`
}

// manualRunRequest represents a request to manually trigger a workflow run.
type manualRunRequest struct {
	WorkflowID string `json:"workflow_id"`
}

// --- Workflows ---

func (a *API) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	workflows, err := a.listWorkflowDefinitions(r.Context(), user)
	if err != nil {
		log.Printf("error: listing workflow definitions: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"workflows": workflows,
		"count":     len(workflows),
	})
}

func (a *API) handleWorkflowByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse: /api/v1/workflows/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
	if path == "" {
		respondError(w, http.StatusBadRequest, "workflow id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	workflow, err := a.getWorkflowDefinitionDetail(r.Context(), path, user)
	if err != nil {
		respondError(w, http.StatusNotFound, "workflow not found")
		return
	}

	respondJSON(w, http.StatusOK, workflow)
}

// --- Workflow Runs ---

func (a *API) handleWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List workflow runs with optional status filter
		query := r.URL.Query()
		status := query.Get("status")

		pageStr := query.Get("page")
		perPageStr := query.Get("per_page")

		page := 1
		perPage := 50
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
		if pp, err := strconv.Atoi(perPageStr); err == nil && pp > 0 && pp <= 100 {
			perPage = pp
		}

		runs, total, err := a.listWorkflowRuns(r.Context(), user, status, page, perPage)
		if err != nil {
			log.Printf("error: listing workflow runs: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list workflow runs")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"workflow_runs": runs,
			"count":         len(runs),
			"total":         total,
			"page":          page,
			"per_page":      perPage,
			"pages":         (total + perPage - 1) / perPage,
		})

	case http.MethodPost:
		// Check system mode before triggering.
		if mode, _ := a.settingsStore.GetSystemMode(r.Context()); mode == "paused" {
			respondError(w, http.StatusServiceUnavailable, "system is paused for maintenance — new workflow runs are not being accepted")
			return
		}

		// Manually trigger a workflow run
		var req manualRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if req.WorkflowID == "" {
			respondError(w, http.StatusBadRequest, "workflow_id is required")
			return
		}

		// Check if workflow exists and user has access
		_, err := a.getWorkflowDefinitionDetail(r.Context(), req.WorkflowID, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "workflow not found or access denied")
			return
		}

		// Start the workflow run
		run, err := a.startWorkflowRun(r.Context(), req.WorkflowID, "manual", "", user)
		if err != nil {
			log.Printf("error: starting workflow run: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to start workflow run: "+err.Error())
			return
		}

		respondJSON(w, http.StatusCreated, run)

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleWorkflowRunByID(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/workflow-runs/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflow-runs/")
	if path == "" {
		respondError(w, http.StatusBadRequest, "workflow run id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Get run detail with all steps, outputs, status
		run, err := a.getWorkflowRunDetail(r.Context(), path, user)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				respondError(w, http.StatusNotFound, "workflow run not found")
			} else {
				log.Printf("error: getting workflow run detail: %v", err)
				respondError(w, http.StatusInternalServerError, "failed to get workflow run")
			}
			return
		}

		respondJSON(w, http.StatusOK, run)

	case http.MethodDelete:
		// Cancel a running workflow
		err := a.cancelWorkflowRun(r.Context(), path, user)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				respondError(w, http.StatusNotFound, "workflow run not found")
			} else if strings.Contains(err.Error(), "not running") {
				respondError(w, http.StatusBadRequest, "workflow run is not running")
			} else {
				log.Printf("error: cancelling workflow run: %v", err)
				respondError(w, http.StatusInternalServerError, "failed to cancel workflow run")
			}
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"cancelled": true,
			"run_id":    path,
		})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Database queries and business logic ---

func (a *API) listWorkflowDefinitions(ctx context.Context, owner string) ([]workflowDefinitionResponse, error) {
	rows, err := a.db.Query(ctx, `
		SELECT id, name, source_repo, source_file, raw_yaml, parsed, last_synced, owner
		FROM workflows
		WHERE owner = $1
		ORDER BY name ASC
	`, owner)
	if err != nil {
		return nil, fmt.Errorf("querying workflows: %w", err)
	}
	defer rows.Close()

	var workflows []workflowDefinitionResponse
	for rows.Next() {
		var id, name, sourceRepo, sourceFile, rawYAML, ownerField string
		var parsedJSON []byte
		var lastSynced time.Time

		if err := rows.Scan(&id, &name, &sourceRepo, &sourceFile, &rawYAML, &parsedJSON, &lastSynced, &ownerField); err != nil {
			return nil, fmt.Errorf("scanning workflow: %w", err)
		}

		wd := workflowDefinitionResponse{
			ID:         id,
			Name:       name,
			SourceRepo: sourceRepo,
			SourceFile: sourceFile,
			Owner:      ownerField,
			LastSynced: &lastSynced,
		}

		// Parse workflow steps from JSON
		if parsedJSON != nil {
			var storedWd WorkflowDefinition
			if err := json.Unmarshal(parsedJSON, &storedWd); err == nil {
				// Convert workflow steps to generic map for API response
				var workflowSteps []map[string]interface{}
				for _, step := range storedWd.Workflow {
					stepMap := map[string]interface{}{
						"id":    step.ID,
						"agent": step.Agent,
					}
					if step.Repo != "" {
						stepMap["repo"] = step.Repo
					}
					if len(step.Needs) > 0 {
						stepMap["needs"] = step.Needs
					}
					if step.Condition != "" {
						stepMap["condition"] = step.Condition
					}
					if step.Approval != "" {
						stepMap["approval"] = step.Approval
					}
					if len(step.Outputs) > 0 {
						stepMap["outputs"] = step.Outputs
					}
					if len(step.Inputs) > 0 {
						stepMap["inputs"] = step.Inputs
					}
					workflowSteps = append(workflowSteps, stepMap)
				}
				wd.Workflow = workflowSteps
			}
		}

		workflows = append(workflows, wd)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workflows: %w", err)
	}

	if workflows == nil {
		workflows = []workflowDefinitionResponse{}
	}

	return workflows, nil
}

func (a *API) getWorkflowDefinitionDetail(ctx context.Context, workflowID, owner string) (*workflowDefinitionResponse, error) {
	// Get workflow definition by ID from database
	var id, name, sourceRepo, sourceFile, rawYAML, ownerField string
	var parsedJSON []byte
	var lastSynced time.Time

	err := a.db.QueryRow(ctx, `
		SELECT id, name, source_repo, source_file, raw_yaml, parsed, last_synced, owner
		FROM workflows
		WHERE id = $1 AND owner = $2
	`, workflowID, owner).Scan(&id, &name, &sourceRepo, &sourceFile, &rawYAML, &parsedJSON, &lastSynced, &ownerField)
	if err != nil {
		return nil, fmt.Errorf("workflow not found: %w", err)
	}

	wd := &workflowDefinitionResponse{
		ID:         id,
		Name:       name,
		SourceRepo: sourceRepo,
		SourceFile: sourceFile,
		Owner:      ownerField,
		LastSynced: &lastSynced,
	}

	// Parse workflow steps from JSON
	if parsedJSON != nil {
		var storedWd WorkflowDefinition
		if err := json.Unmarshal(parsedJSON, &storedWd); err == nil {
			// Convert workflow steps to generic map for API response
			var workflowSteps []map[string]interface{}
			for _, step := range storedWd.Workflow {
				stepMap := map[string]interface{}{
					"id":    step.ID,
					"agent": step.Agent,
				}
				if step.Repo != "" {
					stepMap["repo"] = step.Repo
				}
				if len(step.Needs) > 0 {
					stepMap["needs"] = step.Needs
				}
				if step.Condition != "" {
					stepMap["condition"] = step.Condition
				}
				if step.Approval != "" {
					stepMap["approval"] = step.Approval
				}
				if len(step.Outputs) > 0 {
					stepMap["outputs"] = step.Outputs
				}
				if len(step.Inputs) > 0 {
					stepMap["inputs"] = step.Inputs
				}
				workflowSteps = append(workflowSteps, stepMap)
			}
			wd.Workflow = workflowSteps
		}
	}

	return wd, nil
}

func (a *API) listWorkflowRuns(ctx context.Context, owner, status string, page, perPage int) ([]workflowRunListItem, int, error) {
	whereClause := " WHERE wr.owner = $1"
	args := []any{owner}
	argN := 2

	if status != "" {
		whereClause += fmt.Sprintf(" AND wr.status = $%d", argN)
		args = append(args, status)
		argN++
	}

	// Count total matching runs
	countQuery := `SELECT COUNT(*) FROM workflow_runs wr` + whereClause
	countArgs := make([]any, len(args))
	copy(countArgs, args)

	var total int
	err := a.db.QueryRow(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting workflow runs: %w", err)
	}

	// Main query with pagination
	query := `
		SELECT wr.id, wr.workflow_id, w.name, wr.status, wr.started_at, wr.finished_at, wr.trigger_type, wr.trigger_ref, wr.owner
		FROM workflow_runs wr
		LEFT JOIN workflows w ON wr.workflow_id = w.id
	` + whereClause + " ORDER BY wr.created_at DESC"

	offset := (page - 1) * perPage
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, perPage, offset)

	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying workflow runs: %w", err)
	}
	defer rows.Close()

	var runs []workflowRunListItem
	for rows.Next() {
		var run workflowRunListItem
		var workflowName *string
		var startedAt, finishedAt *time.Time

		if err := rows.Scan(&run.ID, &run.WorkflowID, &workflowName, &run.Status,
			&startedAt, &finishedAt, &run.TriggerType, &run.TriggerRef, &run.Owner); err != nil {
			return nil, 0, fmt.Errorf("scanning workflow run: %w", err)
		}

		if workflowName != nil {
			run.Workflow = *workflowName
		} else {
			run.Workflow = "Unknown Workflow"
		}

		run.StartedAt = startedAt
		run.FinishedAt = finishedAt

		runs = append(runs, run)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating workflow runs: %w", err)
	}

	if runs == nil {
		runs = []workflowRunListItem{}
	}

	return runs, total, nil
}

func (a *API) getWorkflowRunDetail(ctx context.Context, runID, owner string) (*workflowRunDetailResponse, error) {
	// Get the workflow run
	var workflowID, status, triggerType, triggerRef string
	var workflowName *string
	var startedAt, finishedAt *time.Time

	err := a.db.QueryRow(ctx, `
		SELECT wr.workflow_id, w.name, wr.status, wr.started_at, wr.finished_at, wr.trigger_type, wr.trigger_ref
		FROM workflow_runs wr
		LEFT JOIN workflows w ON wr.workflow_id = w.id
		WHERE wr.id = $1 AND wr.owner = $2
	`, runID, owner).Scan(&workflowID, &workflowName, &status, &startedAt, &finishedAt, &triggerType, &triggerRef)
	if err != nil {
		return nil, fmt.Errorf("workflow run not found: %w", err)
	}

	workflow := "Unknown Workflow"
	if workflowName != nil {
		workflow = *workflowName
	}

	response := &workflowRunDetailResponse{
		ID:          runID,
		Workflow:    workflow,
		Status:      status,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		TriggerType: triggerType,
		TriggerRef:  triggerRef,
	}

	// Get all steps for this run with session details
	steps, err := a.getWorkflowRunStepsWithTokens(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting workflow run steps: %w", err)
	}

	response.Steps = steps
	return response, nil
}

func (a *API) getWorkflowRunStepsWithTokens(ctx context.Context, runID string) ([]workflowRunStepDetail, error) {
	query := `
		SELECT wrs.step_id, wrs.status, wrs.session_id, wrs.outputs, wrs.started_at, wrs.finished_at,
		       s.proxy_log
		FROM workflow_run_steps wrs
		LEFT JOIN sessions s ON wrs.session_id = s.id
		WHERE wrs.run_id = $1
		ORDER BY wrs.step_id
	`

	rows, err := a.db.Query(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("querying workflow run steps: %w", err)
	}
	defer rows.Close()

	var steps []workflowRunStepDetail
	for rows.Next() {
		var step workflowRunStepDetail
		var sessionID *string
		var outputsJSON, proxyLogJSON []byte
		var startedAt, finishedAt *time.Time

		if err := rows.Scan(&step.ID, &step.Status, &sessionID, &outputsJSON,
			&startedAt, &finishedAt, &proxyLogJSON); err != nil {
			return nil, fmt.Errorf("scanning workflow run step: %w", err)
		}

		if sessionID != nil {
			step.SessionID = *sessionID
		}

		step.StartedAt = startedAt
		step.FinishedAt = finishedAt

		// Calculate duration
		if startedAt != nil && finishedAt != nil {
			duration := finishedAt.Sub(*startedAt)
			step.DurationSeconds = int64(duration.Seconds())
		}

		// Parse outputs
		if outputsJSON != nil {
			if err := json.Unmarshal(outputsJSON, &step.Outputs); err != nil {
				log.Printf("error unmarshaling step outputs: %v", err)
				step.Outputs = make(map[string]interface{})
			}
		} else {
			step.Outputs = make(map[string]interface{})
		}

		// Extract token counts from proxy log
		if proxyLogJSON != nil {
			step.TokensIn, step.TokensOut = a.extractTokenCounts(proxyLogJSON)
		}

		steps = append(steps, step)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workflow run steps: %w", err)
	}

	if steps == nil {
		steps = []workflowRunStepDetail{}
	}

	return steps, nil
}

func (a *API) extractTokenCounts(proxyLogJSON []byte) (tokensIn, tokensOut int64) {
	// Parse proxy log to extract token counts
	var entries []map[string]interface{}
	if err := json.Unmarshal(proxyLogJSON, &entries); err != nil {
		return 0, 0
	}

	for _, entry := range entries {
		// Look for LLM proxy requests with token information
		if method, ok := entry["method"].(string); ok && method == "POST" {
			if url, ok := entry["url"].(string); ok && strings.Contains(url, "v1/messages") {
				// This is likely an Anthropic API call - look for usage info
				if usage, ok := entry["usage"].(map[string]interface{}); ok {
					if inputTokens, ok := usage["input_tokens"].(float64); ok {
						tokensIn += int64(inputTokens)
					}
					if outputTokens, ok := usage["output_tokens"].(float64); ok {
						tokensOut += int64(outputTokens)
					}
				}
			}
		}
	}

	return tokensIn, tokensOut
}

func (a *API) startWorkflowRun(ctx context.Context, workflowID, triggerType, triggerRef, owner string) (*workflowRunListItem, error) {
	// Use the workflow engine to start the run - for now we'll need to access it through the dispatcher
	// First get the workflow definition
	var workflowName string
	var parsedJSON []byte
	err := a.db.QueryRow(ctx, `SELECT name, parsed FROM workflows WHERE id = $1`, workflowID).Scan(&workflowName, &parsedJSON)
	if err != nil {
		return nil, fmt.Errorf("workflow not found: %w", err)
	}

	// For now, create a basic workflow run record since we need the workflow engine
	// This is a simplified implementation that would be enhanced with full workflow engine integration
	runID := "run-" + strings.ReplaceAll(workflowID, "-", "")[:8] + "-" + fmt.Sprintf("%d", time.Now().Unix())
	now := time.Now().UTC()

	_, err = a.db.Exec(ctx, `
		INSERT INTO workflow_runs (id, workflow_id, status, trigger_type, trigger_ref, owner, created_at)
		VALUES ($1, $2, 'pending', $3, $4, $5, $6)
	`, runID, workflowID, triggerType, triggerRef, owner, now)
	if err != nil {
		return nil, fmt.Errorf("creating workflow run: %w", err)
	}

	return &workflowRunListItem{
		ID:          runID,
		WorkflowID:  workflowID,
		Workflow:    workflowName,
		Status:      "pending",
		TriggerType: triggerType,
		TriggerRef:  triggerRef,
		Owner:       owner,
	}, nil
}

func (a *API) cancelWorkflowRun(ctx context.Context, runID, owner string) error {
	// Check if the run exists and is owned by the user
	var status string
	err := a.db.QueryRow(ctx, `
		SELECT status FROM workflow_runs WHERE id = $1 AND owner = $2
	`, runID, owner).Scan(&status)
	if err != nil {
		return fmt.Errorf("workflow run not found")
	}

	if status != "running" && status != "pending" {
		return fmt.Errorf("workflow run is not running")
	}

	// Get all running steps and cancel their sessions
	rows, err := a.db.Query(ctx, `
		SELECT session_id FROM workflow_run_steps 
		WHERE run_id = $1 AND status = 'running' AND session_id IS NOT NULL
	`, runID)
	if err != nil {
		return fmt.Errorf("getting running steps: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			continue
		}
		sessionIDs = append(sessionIDs, sessionID)
	}

	// Cancel all running sessions
	for _, sessionID := range sessionIDs {
		if err := a.dispatcher.CancelSession(ctx, sessionID); err != nil {
			log.Printf("error cancelling session %s: %v", sessionID, err)
		}
	}

	// Update workflow run status
	now := time.Now().UTC()
	_, err = a.db.Exec(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', finished_at = $2 WHERE id = $1
	`, runID, now)
	if err != nil {
		return fmt.Errorf("updating workflow run status: %w", err)
	}

	// Update all pending/running steps to cancelled
	_, err = a.db.Exec(ctx, `
		UPDATE workflow_run_steps 
		SET status = 'cancelled', finished_at = $2 
		WHERE run_id = $1 AND status IN ('pending', 'running', 'awaiting_approval')
	`, runID, now)
	if err != nil {
		return fmt.Errorf("updating workflow steps status: %w", err)
	}

	return nil
}
