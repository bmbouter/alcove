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

// WorkflowAPIHandlers contains workflow-specific route handlers
type WorkflowAPIHandlers struct {
	api            *API
	workflowStore  *WorkflowStore
	workflowEngine *WorkflowEngine
}

// NewWorkflowAPIHandlers creates a new WorkflowAPIHandlers instance
func NewWorkflowAPIHandlers(api *API, workflowStore *WorkflowStore, workflowEngine *WorkflowEngine) *WorkflowAPIHandlers {
	return &WorkflowAPIHandlers{
		api:            api,
		workflowStore:  workflowStore,
		workflowEngine: workflowEngine,
	}
}

// RegisterWorkflowRoutes registers workflow API routes
func (w *WorkflowAPIHandlers) RegisterWorkflowRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/workflows", w.handleWorkflows)
	mux.HandleFunc("/api/v1/workflows/", w.handleWorkflowByID)
	mux.HandleFunc("/api/v1/workflow-runs", w.handleWorkflowRuns)
	mux.HandleFunc("/api/v1/workflow-runs/", w.handleWorkflowRunByID)
}

// --- Workflows ---

func (w *WorkflowAPIHandlers) handleWorkflows(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.respondError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		w.respondError(rw, http.StatusUnauthorized, "authentication required")
		return
	}

	workflows, err := w.listWorkflowDefinitions(r.Context(), user)
	if err != nil {
		log.Printf("error: listing workflow definitions: %v", err)
		w.respondError(rw, http.StatusInternalServerError, "failed to list workflows")
		return
	}

	w.respondJSON(rw, http.StatusOK, map[string]any{
		"workflows": workflows,
		"count":     len(workflows),
	})
}

func (w *WorkflowAPIHandlers) handleWorkflowByID(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.respondError(rw, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Parse: /api/v1/workflows/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
	if path == "" {
		w.respondError(rw, http.StatusBadRequest, "workflow id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		w.respondError(rw, http.StatusUnauthorized, "authentication required")
		return
	}

	workflow, err := w.getWorkflowDefinitionDetail(r.Context(), path, user)
	if err != nil {
		w.respondError(rw, http.StatusNotFound, "workflow not found")
		return
	}

	w.respondJSON(rw, http.StatusOK, workflow)
}

// --- Workflow Runs ---

func (w *WorkflowAPIHandlers) handleWorkflowRuns(rw http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		w.respondError(rw, http.StatusUnauthorized, "authentication required")
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

		runs, total, err := w.listWorkflowRuns(r.Context(), user, status, page, perPage)
		if err != nil {
			log.Printf("error: listing workflow runs: %v", err)
			w.respondError(rw, http.StatusInternalServerError, "failed to list workflow runs")
			return
		}

		w.respondJSON(rw, http.StatusOK, map[string]any{
			"workflow_runs": runs,
			"count":         len(runs),
			"total":         total,
			"page":          page,
			"per_page":      perPage,
			"pages":         (total + perPage - 1) / perPage,
		})

	case http.MethodPost:
		// Check system mode before triggering.
		if mode, _ := w.api.settingsStore.GetSystemMode(r.Context()); mode == "paused" {
			w.respondError(rw, http.StatusServiceUnavailable, "system is paused for maintenance — new workflow runs are not being accepted")
			return
		}

		// Manually trigger a workflow run
		var req manualRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.respondError(rw, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if req.WorkflowID == "" {
			w.respondError(rw, http.StatusBadRequest, "workflow_id is required")
			return
		}

		// Check if workflow exists and user has access
		_, err := w.getWorkflowDefinitionDetail(r.Context(), req.WorkflowID, user)
		if err != nil {
			w.respondError(rw, http.StatusNotFound, "workflow not found or access denied")
			return
		}

		// Start the workflow run
		run, err := w.startWorkflowRun(r.Context(), req.WorkflowID, "manual", "", user)
		if err != nil {
			log.Printf("error: starting workflow run: %v", err)
			w.respondError(rw, http.StatusInternalServerError, "failed to start workflow run: "+err.Error())
			return
		}

		w.respondJSON(rw, http.StatusCreated, run)

	default:
		w.respondError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (w *WorkflowAPIHandlers) handleWorkflowRunByID(rw http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/workflow-runs/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflow-runs/")
	if path == "" {
		w.respondError(rw, http.StatusBadRequest, "workflow run id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		w.respondError(rw, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Get run detail with all steps, outputs, status
		run, err := w.getWorkflowRunDetail(r.Context(), path, user)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				w.respondError(rw, http.StatusNotFound, "workflow run not found")
			} else {
				log.Printf("error: getting workflow run detail: %v", err)
				w.respondError(rw, http.StatusInternalServerError, "failed to get workflow run")
			}
			return
		}

		w.respondJSON(rw, http.StatusOK, run)

	case http.MethodDelete:
		// Cancel a running workflow
		err := w.cancelWorkflowRun(r.Context(), path, user)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				w.respondError(rw, http.StatusNotFound, "workflow run not found")
			} else if strings.Contains(err.Error(), "not running") {
				w.respondError(rw, http.StatusBadRequest, "workflow run is not running")
			} else {
				log.Printf("error: cancelling workflow run: %v", err)
				w.respondError(rw, http.StatusInternalServerError, "failed to cancel workflow run")
			}
			return
		}

		w.respondJSON(rw, http.StatusOK, map[string]any{
			"cancelled": true,
			"run_id":    path,
		})

	default:
		w.respondError(rw, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Helper methods ---

func (w *WorkflowAPIHandlers) respondJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(v)
}

func (w *WorkflowAPIHandlers) respondError(rw http.ResponseWriter, code int, msg string) {
	w.respondJSON(rw, code, map[string]string{"error": msg})
}

// --- Database queries and business logic ---

func (w *WorkflowAPIHandlers) listWorkflowDefinitions(ctx context.Context, owner string) ([]workflowDefinitionResponse, error) {
	rows, err := w.api.db.Query(ctx, `
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

func (w *WorkflowAPIHandlers) getWorkflowDefinitionDetail(ctx context.Context, workflowID, owner string) (*workflowDefinitionResponse, error) {
	// Get workflow definition by ID from database
	var id, name, sourceRepo, sourceFile, rawYAML, ownerField string
	var parsedJSON []byte
	var lastSynced time.Time

	err := w.api.db.QueryRow(ctx, `
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

func (w *WorkflowAPIHandlers) listWorkflowRuns(ctx context.Context, owner, status string, page, perPage int) ([]workflowRunListItem, int, error) {
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
	err := w.api.db.QueryRow(ctx, countQuery, countArgs...).Scan(&total)
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

	rows, err := w.api.db.Query(ctx, query, args...)
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

func (w *WorkflowAPIHandlers) getWorkflowRunDetail(ctx context.Context, runID, owner string) (*workflowRunDetailResponse, error) {
	// Get the workflow run
	var workflowID, status, triggerType, triggerRef string
	var workflowName *string
	var startedAt, finishedAt *time.Time

	err := w.api.db.QueryRow(ctx, `
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
	steps, err := w.getWorkflowRunStepsWithTokens(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting workflow run steps: %w", err)
	}

	response.Steps = steps
	return response, nil
}

func (w *WorkflowAPIHandlers) getWorkflowRunStepsWithTokens(ctx context.Context, runID string) ([]workflowRunStepDetail, error) {
	query := `
		SELECT wrs.step_id, wrs.status, wrs.session_id, wrs.outputs, wrs.started_at, wrs.finished_at,
		       s.proxy_log
		FROM workflow_run_steps wrs
		LEFT JOIN sessions s ON wrs.session_id = s.id
		WHERE wrs.run_id = $1
		ORDER BY wrs.step_id
	`

	rows, err := w.api.db.Query(ctx, query, runID)
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
			step.TokensIn, step.TokensOut = w.extractTokenCounts(proxyLogJSON)
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

func (w *WorkflowAPIHandlers) extractTokenCounts(proxyLogJSON []byte) (tokensIn, tokensOut int64) {
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

func (w *WorkflowAPIHandlers) startWorkflowRun(ctx context.Context, workflowID, triggerType, triggerRef, owner string) (*workflowRunListItem, error) {
	// Use the workflow engine to start the run
	if w.workflowEngine != nil {
		run, err := w.workflowEngine.StartWorkflowRun(ctx, workflowID, triggerType, triggerRef, owner)
		if err != nil {
			return nil, fmt.Errorf("starting workflow run: %w", err)
		}

		// Get workflow name for response
		var workflowName string
		err = w.api.db.QueryRow(ctx, `SELECT name FROM workflows WHERE id = $1`, workflowID).Scan(&workflowName)
		if err != nil {
			workflowName = "Unknown Workflow"
		}

		return &workflowRunListItem{
			ID:          run.ID,
			WorkflowID:  run.WorkflowID,
			Workflow:    workflowName,
			Status:      run.Status,
			StartedAt:   run.StartedAt,
			TriggerType: run.TriggerType,
			TriggerRef:  run.TriggerRef,
			Owner:       run.Owner,
		}, nil
	}

	// Fallback implementation if workflow engine is not available
	return w.createBasicWorkflowRun(ctx, workflowID, triggerType, triggerRef, owner)
}

func (w *WorkflowAPIHandlers) createBasicWorkflowRun(ctx context.Context, workflowID, triggerType, triggerRef, owner string) (*workflowRunListItem, error) {
	// Get the workflow definition
	var workflowName string
	var parsedJSON []byte
	err := w.api.db.QueryRow(ctx, `SELECT name, parsed FROM workflows WHERE id = $1`, workflowID).Scan(&workflowName, &parsedJSON)
	if err != nil {
		return nil, fmt.Errorf("workflow not found: %w", err)
	}

	// Create a basic workflow run record
	runID := "run-" + strings.ReplaceAll(workflowID, "-", "")[:8] + "-" + fmt.Sprintf("%d", time.Now().Unix())
	now := time.Now().UTC()

	_, err = w.api.db.Exec(ctx, `
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

func (w *WorkflowAPIHandlers) cancelWorkflowRun(ctx context.Context, runID, owner string) error {
	// Check if the run exists and is owned by the user
	var status string
	err := w.api.db.QueryRow(ctx, `
		SELECT status FROM workflow_runs WHERE id = $1 AND owner = $2
	`, runID, owner).Scan(&status)
	if err != nil {
		return fmt.Errorf("workflow run not found")
	}

	if status != "running" && status != "pending" {
		return fmt.Errorf("workflow run is not running")
	}

	// Get all running steps and cancel their sessions
	rows, err := w.api.db.Query(ctx, `
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
		if err := w.api.dispatcher.CancelSession(ctx, sessionID); err != nil {
			log.Printf("error cancelling session %s: %v", sessionID, err)
		}
	}

	// Update workflow run status
	now := time.Now().UTC()
	_, err = w.api.db.Exec(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', finished_at = $2 WHERE id = $1
	`, runID, now)
	if err != nil {
		return fmt.Errorf("updating workflow run status: %w", err)
	}

	// Update all pending/running steps to cancelled
	_, err = w.api.db.Exec(ctx, `
		UPDATE workflow_run_steps 
		SET status = 'cancelled', finished_at = $2 
		WHERE run_id = $1 AND status IN ('pending', 'running', 'awaiting_approval')
	`, runID, now)
	if err != nil {
		return fmt.Errorf("updating workflow steps status: %w", err)
	}

	return nil
}
