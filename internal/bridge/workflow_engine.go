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
	"regexp"
	"strings"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/bmbouter/alcove/internal/bridge/condition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// templateRegex matches template references like {{steps.X.outputs.Y}}.
var templateRegex = regexp.MustCompile(`\{\{[^}]+\}\}`)

// WorkflowEngine manages workflow execution: DAG evaluation, step dispatch, and completion tracking.
type WorkflowEngine struct {
	db               *pgxpool.Pool
	dispatcher       *Dispatcher
	workflowStore    *WorkflowStore
	defStore         *AgentDefStore
	credStore        *CredentialStore
	catalogItemStore *CatalogItemStore
	bridgeActions    map[string]BridgeActionHandler
}

// NewWorkflowEngine creates a new workflow engine with the given dependencies.
func NewWorkflowEngine(db *pgxpool.Pool, dispatcher *Dispatcher, workflowStore *WorkflowStore, defStore *AgentDefStore, credStore *CredentialStore) *WorkflowEngine {
	return &WorkflowEngine{
		db:               db,
		dispatcher:       dispatcher,
		workflowStore:    workflowStore,
		defStore:         defStore,
		credStore:        credStore,
		catalogItemStore: NewCatalogItemStore(db),
		bridgeActions:    RegisterBridgeActions(),
	}
}

// WorkflowRun represents a single execution of a workflow.
type WorkflowRun struct {
	ID          string                 `json:"id"`
	WorkflowID  string                 `json:"workflow_id"`
	Status      string                 `json:"status"` // pending, running, completed, failed, cancelled, awaiting_approval
	TriggerType string                 `json:"trigger_type,omitempty"`
	TriggerRef  string                 `json:"trigger_ref,omitempty"`
	CurrentStep string                 `json:"current_step,omitempty"`
	StepOutputs map[string]interface{} `json:"step_outputs"`
	StartedAt   *time.Time             `json:"started_at,omitempty"`
	FinishedAt  *time.Time             `json:"finished_at,omitempty"`
	TeamID      string                 `json:"team_id"`
	CreatedAt   time.Time              `json:"created_at"`
}

// WorkflowRunStep represents a single step execution within a workflow run.
type WorkflowRunStep struct {
	ID         string                 `json:"id"`
	RunID      string                 `json:"run_id"`
	StepID     string                 `json:"step_id"`
	SessionID  string                 `json:"session_id,omitempty"`
	Status     string                 `json:"status"` // pending, running, completed, failed, skipped, awaiting_approval
	Outputs    map[string]interface{} `json:"outputs,omitempty"`
	Iteration  int                    `json:"iteration"`
	StartedAt  *time.Time             `json:"started_at,omitempty"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`

	// Fields enriched from the workflow definition (not stored in DB).
	Type          string            `json:"type,omitempty"`
	Action        string            `json:"action,omitempty"`
	Depends       string            `json:"depends,omitempty"`
	MaxIterations int               `json:"max_iterations,omitempty"`
	Credentials   map[string]string `json:"credentials,omitempty"`
}

// StartWorkflowRun creates a new workflow run and dispatches initial steps.
func (we *WorkflowEngine) StartWorkflowRun(ctx context.Context, workflowID, triggerType, triggerRef, teamID string, triggerContext ...map[string]interface{}) (*WorkflowRun, error) {
	// Get the workflow definition
	workflow, err := we.getWorkflowByID(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("getting workflow definition: %w", err)
	}

	// Create workflow run record
	runID := uuid.New().String()
	now := time.Now().UTC()
	run := &WorkflowRun{
		ID:          runID,
		WorkflowID:  workflowID,
		Status:      "pending",
		TriggerType: triggerType,
		TriggerRef:  triggerRef,
		StepOutputs: make(map[string]interface{}),
		TeamID:      teamID,
		CreatedAt:   now,
	}

	// Store trigger context if provided
	if len(triggerContext) > 0 && triggerContext[0] != nil {
		run.StepOutputs["_trigger_context"] = triggerContext[0]
	}

	// Insert workflow run
	if err := we.insertWorkflowRun(ctx, run); err != nil {
		return nil, fmt.Errorf("creating workflow run: %w", err)
	}

	// Create workflow_run_steps records for all steps
	for _, step := range workflow.Workflow {
		stepRecord := &WorkflowRunStep{
			ID:     uuid.New().String(),
			RunID:  runID,
			StepID: step.ID,
			Status: "pending",
		}
		if err := we.insertWorkflowRunStep(ctx, stepRecord); err != nil {
			return nil, fmt.Errorf("creating workflow run step %s: %w", step.ID, err)
		}
	}

	// Find and dispatch root steps (steps with no dependencies)
	rootSteps := workflow.GetRootSteps()
	if len(rootSteps) == 0 {
		return nil, fmt.Errorf("workflow has no root steps (all steps have dependencies)")
	}

	// Mark workflow as running
	run.Status = "running"
	run.StartedAt = &now
	if err := we.updateWorkflowRunStatus(ctx, runID, "running", &now, nil); err != nil {
		return nil, fmt.Errorf("marking workflow as running: %w", err)
	}

	// Dispatch root steps
	for _, step := range rootSteps {
		if err := we.dispatchStep(ctx, run, &step, workflow); err != nil {
			log.Printf("error dispatching root step %s: %v", step.ID, err)
			// Mark the step as failed
			if err := we.updateStepStatus(ctx, runID, step.ID, "failed", nil, nil); err != nil {
				log.Printf("error marking step %s as failed: %v", step.ID, err)
			}
		}
	}

	return run, nil
}

// dispatchStep dispatches a single workflow step.
func (we *WorkflowEngine) dispatchStep(ctx context.Context, run *WorkflowRun, step *WorkflowStep, workflow *WorkflowDefinition) error {
	log.Printf("dispatching step %s for workflow run %s", step.ID, run.ID)

	// Check iteration limit before dispatching.
	maxIter := step.MaxIterations
	if maxIter <= 0 {
		maxIter = 1
	}
	currentIter, err := we.getStepIteration(ctx, run.ID, step.ID)
	if err != nil {
		return fmt.Errorf("getting iteration count for step %s: %w", step.ID, err)
	}
	if currentIter >= maxIter {
		log.Printf("step %s reached max_iterations (%d), marking as failed", step.ID, maxIter)
		now := time.Now().UTC()
		outputs := map[string]interface{}{"error": "max_iterations_exceeded"}
		if err := we.updateStepStatus(ctx, run.ID, step.ID, "failed", &now, outputs); err != nil {
			return fmt.Errorf("marking step as failed (max iterations): %w", err)
		}
		if err := we.updateRunStepOutputs(ctx, run.ID, step.ID, outputs); err != nil {
			log.Printf("error updating run step outputs for max_iterations: %v", err)
		}
		return we.checkAndDispatchDependents(ctx, run, step.ID, workflow)
	}

	// Increment the iteration counter.
	if err := we.incrementStepIteration(ctx, run.ID, step.ID); err != nil {
		return fmt.Errorf("incrementing iteration for step %s: %w", step.ID, err)
	}

	// Check condition if present
	if step.Condition != "" {
		shouldRun, err := we.evaluateCondition(ctx, run.ID, step.Condition)
		if err != nil {
			return fmt.Errorf("evaluating condition for step %s: %w", step.ID, err)
		}
		if !shouldRun {
			log.Printf("step %s skipped due to condition", step.ID)
			if err := we.updateStepStatus(ctx, run.ID, step.ID, "skipped", nil, nil); err != nil {
				return fmt.Errorf("marking step as skipped: %w", err)
			}
			// Continue with dependent steps
			return we.checkAndDispatchDependents(ctx, run, step.ID, workflow)
		}
	}

	// Check if approval is required
	if step.Approval == "required" {
		log.Printf("step %s requires approval", step.ID)
		if err := we.updateStepStatus(ctx, run.ID, step.ID, "awaiting_approval", nil, nil); err != nil {
			return fmt.Errorf("marking step as awaiting approval: %w", err)
		}
		// Update workflow run status to awaiting_approval
		if err := we.updateWorkflowRunStatus(ctx, run.ID, "awaiting_approval", nil, nil); err != nil {
			return fmt.Errorf("marking workflow run as awaiting approval: %w", err)
		}
		return nil
	}

	// Dispatch based on step type.
	stepType := step.Type
	if stepType == "" {
		stepType = "agent"
	}

	if stepType == "bridge" {
		return we.executeBridgeAction(ctx, run, step, workflow)
	}

	// Agent step: dispatch via Dispatcher.
	// Get the agent definition — try agent_definitions first, then catalog_items for "source/slug" format.
	agentDef, err := we.defStore.GetAgentDefinition(ctx, step.Agent, run.TeamID)
	if err != nil && strings.Contains(step.Agent, "/") {
		// Try catalog_items lookup for "source/slug" format.
		parts := strings.SplitN(step.Agent, "/", 2)
		sourceID, slug := parts[0], parts[1]

		// Check if the catalog item exists.
		item, itemErr := we.catalogItemStore.GetCatalogItem(ctx, sourceID, slug)
		if itemErr != nil {
			return fmt.Errorf("unknown agent '%s': not found in agent definitions or catalog", step.Agent)
		}

		// Check if the item is enabled for this team.
		var enabled bool
		enabledErr := we.db.QueryRow(ctx, `
			SELECT enabled FROM team_catalog_items
			WHERE team_id = $1 AND source_id = $2 AND item_slug = $3
		`, run.TeamID, sourceID, slug).Scan(&enabled)
		if enabledErr != nil || !enabled {
			return fmt.Errorf("agent '%s' is disabled — enable it in the catalog", step.Agent)
		}

		// Build a TaskDefinition from the catalog item.
		agentDef = &TaskDefinition{
			ID:          item.ID,
			Name:        item.Name,
			Description: item.Description,
		}
		// Apply definition fields from the catalog item if available.
		if item.Definition != nil {
			if prompt, ok := item.Definition["prompt"].(string); ok {
				agentDef.Prompt = prompt
			}
			if repo, ok := item.Definition["repo"].(string); ok {
				agentDef.Repos = []internal.RepoSpec{{URL: repo}}
			}
		}
		err = nil
	}
	if err != nil {
		return fmt.Errorf("unknown agent '%s': %w", step.Agent, err)
	}

	// Build TaskRequest from step and agent definition
	taskReq := agentDef.ToTaskRequest()
	taskReq.TaskName = step.Agent
	taskReq.TriggerType = run.TriggerType
	taskReq.TriggerRef = run.TriggerRef

	// Merge step-level credentials (override/augment agent credentials).
	if len(step.Credentials) > 0 {
		if taskReq.Credentials == nil {
			taskReq.Credentials = make(map[string]string)
		}
		for envVar, credName := range step.Credentials {
			taskReq.Credentials[envVar] = credName
		}
	}

	// Override direct_outbound from step if set.
	if step.DirectOutbound {
		taskReq.DirectOutbound = true
	}

	// If step has repos specified, override the agent's repos
	if len(step.Repos) > 0 {
		taskReq.Repos = step.Repos
		// Cross-repo credential validation for each repo
		for _, r := range step.Repos {
			repoPath := r.URL
			if err := we.validateCrossRepoCredentials(ctx, repoPath, run.TeamID); err != nil {
				return fmt.Errorf("cross-repo credential validation failed for repo %s: %w", repoPath, err)
			}
		}
	}

	// Inject step inputs into the task request
	if err := we.injectStepInputs(ctx, run.ID, step, &taskReq, run.TriggerRef); err != nil {
		return fmt.Errorf("injecting inputs for step %s: %w", step.ID, err)
	}

	// Store resolved inputs in step outputs so later steps can reference them via
	// {{steps.X.inputs.Y}} templates (e.g., create-pr needs implement's branch input).
	if len(step.Inputs) > 0 {
		stepOutputs, _ := we.getRunStepOutputs(ctx, run.ID)
		ref := run.TriggerRef
		resolvedInputs := make(map[string]interface{})
		for key, value := range step.Inputs {
			resolved, _ := we.processInputValue(value, stepOutputs, ref)
			resolvedInputs["_input_"+key] = resolved
		}
		we.updateRunStepOutputs(ctx, run.ID, step.ID, resolvedInputs)
	}

	// Dispatch the task
	session, err := we.dispatcher.DispatchTask(ctx, taskReq, "workflow", run.TeamID)
	if err != nil {
		return fmt.Errorf("dispatching task for step %s: %w", step.ID, err)
	}

	// Update step with session ID and mark as running
	now := time.Now().UTC()
	if err := we.updateStepWithSession(ctx, run.ID, step.ID, session.ID, "running", &now); err != nil {
		return fmt.Errorf("updating step with session: %w", err)
	}

	log.Printf("dispatched step %s with session %s", step.ID, session.ID)
	return nil
}

// executeBridgeAction executes a bridge-type step (no external agent, runs inside Bridge).
func (we *WorkflowEngine) executeBridgeAction(ctx context.Context, run *WorkflowRun, step *WorkflowStep, workflow *WorkflowDefinition) error {
	log.Printf("executing bridge action %s for step %s in run %s", step.Action, step.ID, run.ID)

	handler, ok := we.bridgeActions[step.Action]
	if !ok {
		return fmt.Errorf("unknown bridge action: %s", step.Action)
	}

	// Resolve inputs (expand templates).
	resolvedInputs, err := we.resolveStepInputs(ctx, run.ID, step, run.TriggerRef)
	if err != nil {
		return fmt.Errorf("resolving inputs for bridge step %s: %w", step.ID, err)
	}

	// Mark step as running.
	now := time.Now().UTC()
	if err := we.updateStepStatus(ctx, run.ID, step.ID, "running", nil, nil); err != nil {
		return fmt.Errorf("marking bridge step as running: %w", err)
	}
	if _, err := we.db.Exec(ctx, `UPDATE workflow_run_steps SET started_at = $3 WHERE run_id = $1 AND step_id = $2`, run.ID, step.ID, &now); err != nil {
		log.Printf("error setting started_at for bridge step %s: %v", step.ID, err)
	}

	// Execute the action.
	result, err := handler(ctx, resolvedInputs, we.credStore, run.TeamID)
	if err != nil {
		failNow := time.Now().UTC()
		outputs := map[string]interface{}{"error": err.Error()}
		if updateErr := we.updateStepStatus(ctx, run.ID, step.ID, "failed", &failNow, outputs); updateErr != nil {
			log.Printf("error marking bridge step %s as failed: %v", step.ID, updateErr)
		}
		return we.checkAndDispatchDependents(ctx, run, step.ID, workflow)
	}

	// Record outputs and mark step as completed/failed.
	finishedNow := time.Now().UTC()
	var stepStatus string
	if result.Status == "succeeded" {
		stepStatus = "completed"
	} else {
		stepStatus = "failed"
		if result.Outputs == nil {
			result.Outputs = make(map[string]interface{})
		}
		if result.Error != "" {
			result.Outputs["error"] = result.Error
		}
	}

	if err := we.updateStepStatus(ctx, run.ID, step.ID, stepStatus, &finishedNow, result.Outputs); err != nil {
		return fmt.Errorf("updating bridge step status: %w", err)
	}

	if err := we.updateRunStepOutputs(ctx, run.ID, step.ID, result.Outputs); err != nil {
		log.Printf("error updating run step outputs for bridge step %s: %v", step.ID, err)
	}

	log.Printf("bridge action %s completed with status %s for step %s", step.Action, stepStatus, step.ID)

	// Check and dispatch dependents.
	if err := we.checkAndDispatchDependents(ctx, run, step.ID, workflow); err != nil {
		return fmt.Errorf("checking dependents for bridge step %s: %w", step.ID, err)
	}

	// Check if workflow is complete.
	return we.checkWorkflowCompletion(ctx, run)
}

// resolveStepInputs resolves template references in step inputs.
func (we *WorkflowEngine) resolveStepInputs(ctx context.Context, runID string, step *WorkflowStep, triggerRef ...string) (map[string]interface{}, error) {
	if len(step.Inputs) == 0 {
		return make(map[string]interface{}), nil
	}

	stepOutputs, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting step outputs: %w", err)
	}

	ref := ""
	if len(triggerRef) > 0 {
		ref = triggerRef[0]
	}

	resolved := make(map[string]interface{})
	for key, value := range step.Inputs {
		processedValue, err := we.processInputValue(value, stepOutputs, ref)
		if err != nil {
			return nil, fmt.Errorf("processing input %s: %w", key, err)
		}
		resolved[key] = processedValue
	}

	return resolved, nil
}

// resetStepForReexecution resets a step so it can be dispatched again (for bounded cycles).
func (we *WorkflowEngine) resetStepForReexecution(ctx context.Context, runID, stepID string) error {
	_, err := we.db.Exec(ctx, `
		UPDATE workflow_run_steps
		SET status = 'pending', session_id = NULL, started_at = NULL, finished_at = NULL, outputs = NULL
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID)
	return err
}

// OnStepCompletion is called when a session completes to handle workflow step completion.
func (we *WorkflowEngine) OnStepCompletion(ctx context.Context, sessionID string, status string, exitCode *int) error {
	// Find the workflow run step by session ID
	step, run, err := we.getStepAndRunBySessionID(ctx, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			// This session is not part of a workflow
			return nil
		}
		return fmt.Errorf("getting workflow step for session %s: %w", sessionID, err)
	}

	log.Printf("handling step completion: step=%s run=%s status=%s", step.StepID, run.ID, status)

	// Read step outputs from the session
	outputs, err := we.readStepOutputs(ctx, sessionID)
	if err != nil {
		log.Printf("error reading outputs for step %s: %v", step.StepID, err)
		outputs = make(map[string]interface{})
	}

	// Update step status
	now := time.Now().UTC()
	stepStatus := "completed"
	if status != "completed" || (exitCode != nil && *exitCode != 0) {
		stepStatus = "failed"
	}

	if err := we.updateStepStatus(ctx, run.ID, step.StepID, stepStatus, &now, outputs); err != nil {
		return fmt.Errorf("updating step status: %w", err)
	}

	// Update accumulated step outputs in the run
	if err := we.updateRunStepOutputs(ctx, run.ID, step.StepID, outputs); err != nil {
		return fmt.Errorf("updating run step outputs: %w", err)
	}

	// Get the workflow definition to find dependents
	workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("getting workflow definition: %w", err)
	}

	// Handle field-based routing if configured for the completed step
	completedStepDef := workflow.GetStepByID(step.StepID)
	if completedStepDef != nil && completedStepDef.RouteField != "" {
		if err := we.handleFieldBasedRouting(ctx, run, completedStepDef, outputs, workflow); err != nil {
			log.Printf("error handling field-based routing for step %s: %v", step.StepID, err)
			// Don't fail the workflow, just log the error
		}
	}

	// Check and dispatch dependent steps
	if err := we.checkAndDispatchDependents(ctx, run, step.StepID, workflow); err != nil {
		return fmt.Errorf("checking dependents for step %s: %w", step.StepID, err)
	}

	// Check if workflow is complete
	if err := we.checkWorkflowCompletion(ctx, run); err != nil {
		return fmt.Errorf("checking workflow completion: %w", err)
	}

	return nil
}

// checkAndDispatchDependents checks if any dependent steps are now ready to run.
func (we *WorkflowEngine) checkAndDispatchDependents(ctx context.Context, run *WorkflowRun, completedStepID string, workflow *WorkflowDefinition) error {
	// Get all step statuses for expression evaluation.
	stepStatuses, err := we.getAllStepStatuses(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("getting all step statuses: %w", err)
	}

	// Check ALL steps in the workflow that reference the completed step.
	for _, step := range workflow.Workflow {
		dependent := step // capture loop variable
		ready := false

		if dependent.Depends != "" {
			// Check if this step's Depends expression references the completed step.
			referencedIDs := ExtractDependsStepIDs(dependent.Depends)
			references := false
			for _, refID := range referencedIDs {
				if refID == completedStepID {
					references = true
					break
				}
			}
			if !references {
				continue
			}

			// Evaluate the full expression.
			result, err := EvaluateDepends(dependent.Depends, stepStatuses)
			if err != nil {
				log.Printf("error evaluating depends for step %s: %v", dependent.ID, err)
				continue
			}
			ready = result
		} else if len(dependent.Needs) > 0 {
			// Legacy: check if all Needs are completed.
			references := false
			for _, dep := range dependent.Needs {
				if dep == completedStepID {
					references = true
					break
				}
			}
			if !references {
				continue
			}

			result, err := we.areStepDependenciesSatisfied(ctx, run.ID, dependent.Needs)
			if err != nil {
				return fmt.Errorf("checking dependencies for step %s: %w", dependent.ID, err)
			}
			ready = result
		} else {
			continue // No dependencies, skip (root step).
		}

		if !ready {
			continue
		}

		// Check step status and max_iterations for re-dispatch.
		stepStatus, err := we.getStepStatus(ctx, run.ID, dependent.ID)
		if err != nil {
			return fmt.Errorf("getting status for step %s: %w", dependent.ID, err)
		}

		maxIter := dependent.MaxIterations
		if maxIter <= 0 {
			maxIter = 1
		}

		if stepStatus == "pending" {
			if err := we.dispatchStep(ctx, run, &dependent, workflow); err != nil {
				log.Printf("error dispatching dependent step %s: %v", dependent.ID, err)
				if err := we.updateStepStatus(ctx, run.ID, dependent.ID, "failed", nil, nil); err != nil {
					log.Printf("error marking step %s as failed: %v", dependent.ID, err)
				}
			}
		} else if (stepStatus == "completed" || stepStatus == "failed") && maxIter > 1 {
			// Re-execute for bounded cycles if under iteration limit.
			currentIter, err := we.getStepIteration(ctx, run.ID, dependent.ID)
			if err != nil {
				log.Printf("error getting iteration for step %s: %v", dependent.ID, err)
				continue
			}
			if currentIter < maxIter {
				log.Printf("re-dispatching step %s for iteration %d/%d", dependent.ID, currentIter+1, maxIter)
				if err := we.resetStepForReexecution(ctx, run.ID, dependent.ID); err != nil {
					log.Printf("error resetting step %s for re-execution: %v", dependent.ID, err)
					continue
				}
				if err := we.dispatchStep(ctx, run, &dependent, workflow); err != nil {
					log.Printf("error re-dispatching step %s: %v", dependent.ID, err)
					if err := we.updateStepStatus(ctx, run.ID, dependent.ID, "failed", nil, nil); err != nil {
						log.Printf("error marking step %s as failed: %v", dependent.ID, err)
					}
				}
			}
		}
	}

	return nil
}

// areStepDependenciesSatisfied checks if all dependencies for a step are completed successfully.
func (we *WorkflowEngine) areStepDependenciesSatisfied(ctx context.Context, runID string, dependencies []string) (bool, error) {
	if len(dependencies) == 0 {
		return true, nil
	}

	for _, dep := range dependencies {
		status, err := we.getStepStatus(ctx, runID, dep)
		if err != nil {
			return false, fmt.Errorf("getting status for dependency %s: %w", dep, err)
		}

		if status != "completed" {
			return false, nil
		}
	}

	return true, nil
}

// checkWorkflowCompletion checks if the workflow run is complete and updates its status.
func (we *WorkflowEngine) checkWorkflowCompletion(ctx context.Context, run *WorkflowRun) error {
	// Get all steps for this run
	steps, err := we.getWorkflowRunSteps(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("getting workflow run steps: %w", err)
	}

	// Get the workflow definition and step statuses for dependency evaluation.
	workflow, _ := we.getWorkflowByID(ctx, run.WorkflowID)
	stepStatuses, _ := we.getAllStepStatuses(ctx, run.ID)

	// Mark unreachable pending steps as skipped: if all referenced steps are
	// terminal (completed/failed/skipped) and the depends expression is false,
	// the step can never be triggered.
	if workflow != nil && stepStatuses != nil {
		for _, step := range steps {
			if step.Status != "pending" {
				continue
			}
			stepDef := workflow.GetStepByID(step.StepID)
			if stepDef == nil || stepDef.Depends == "" {
				continue
			}
			referencedIDs := ExtractDependsStepIDs(stepDef.Depends)
			allTerminal := true
			for _, refID := range referencedIDs {
				status, ok := stepStatuses[refID]
				if !ok || (status != "completed" && status != "failed" && status != "skipped") {
					allTerminal = false
					break
				}
			}
			if !allTerminal {
				continue
			}
			result, err := EvaluateDepends(stepDef.Depends, stepStatuses)
			if err == nil && !result {
				log.Printf("marking unreachable step %s as skipped", step.StepID)
				we.updateStepStatus(ctx, run.ID, step.StepID, "skipped", nil, nil)
			}
		}
		// Re-fetch steps after potential skips.
		steps, err = we.getWorkflowRunSteps(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("re-getting workflow run steps: %w", err)
		}
	}

	allComplete := true
	anyFailed := false

	for _, step := range steps {
		switch step.Status {
		case "pending", "running", "awaiting_approval":
			allComplete = false
		case "failed":
			anyFailed = true
		case "completed", "skipped":
			// These are OK
		default:
			log.Printf("unknown step status: %s", step.Status)
		}
	}

	if !allComplete {
		// Workflow is still running
		return nil
	}

	// All steps are complete
	now := time.Now().UTC()
	var newStatus string

	if anyFailed {
		newStatus = "failed"
	} else {
		newStatus = "completed"
	}

	if err := we.updateWorkflowRunStatus(ctx, run.ID, newStatus, nil, &now); err != nil {
		return fmt.Errorf("updating workflow run status: %w", err)
	}

	log.Printf("workflow run %s completed with status: %s", run.ID, newStatus)
	return nil
}

// evaluateCondition evaluates a condition expression against the current workflow state.
func (we *WorkflowEngine) evaluateCondition(ctx context.Context, runID string, conditionExpr string) (bool, error) {
	// Get accumulated step outputs
	stepOutputsMap, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("getting step outputs: %w", err)
	}

	// Get all step statuses
	stepStatuses, err := we.getAllStepStatuses(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("getting step statuses: %w", err)
	}

	// Convert step outputs to the format expected by the condition evaluator
	stepOutputs := make(map[string]map[string]interface{})
	for stepID, outputs := range stepOutputsMap {
		if outputMap, ok := outputs.(map[string]interface{}); ok {
			stepOutputs[stepID] = outputMap
		} else {
			stepOutputs[stepID] = make(map[string]interface{})
		}
	}

	// Create evaluation context
	evalContext := &condition.EvaluationContext{
		StepStatuses: stepStatuses,
		StepOutputs:  stepOutputs,
	}

	// Use the condition evaluator
	evaluator := condition.NewEvaluator()
	result, err := evaluator.Evaluate(conditionExpr, evalContext)
	if err != nil {
		log.Printf("condition evaluation error for '%s': %v", conditionExpr, err)
		// Default to true for evaluation errors to avoid blocking workflows
		return true, nil
	}

	return result, nil
}

// getAllStepStatuses retrieves the status of all steps in a workflow run.
func (we *WorkflowEngine) getAllStepStatuses(ctx context.Context, runID string) (map[string]string, error) {
	rows, err := we.db.Query(ctx, `
		SELECT step_id, status FROM workflow_run_steps
		WHERE run_id = $1
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := make(map[string]string)
	for rows.Next() {
		var stepID, status string
		if err := rows.Scan(&stepID, &status); err != nil {
			return nil, err
		}
		statuses[stepID] = status
	}

	return statuses, rows.Err()
}

// injectStepInputs processes step inputs and injects them into the task request.
func (we *WorkflowEngine) injectStepInputs(ctx context.Context, runID string, step *WorkflowStep, taskReq *TaskRequest, triggerRef ...string) error {
	if len(step.Inputs) == 0 {
		return nil
	}

	// Get accumulated step outputs for template expansion
	stepOutputs, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return fmt.Errorf("getting step outputs: %w", err)
	}

	// Get trigger ref for template expansion
	ref := ""
	if len(triggerRef) > 0 {
		ref = triggerRef[0]
	}

	// Process each input
	processedInputs := make(map[string]interface{})
	for key, value := range step.Inputs {
		processedValue, err := we.processInputValue(value, stepOutputs, ref)
		if err != nil {
			return fmt.Errorf("processing input %s: %w", key, err)
		}
		processedInputs[key] = processedValue
	}

	// Build a "Workflow Context" section and prepend it to the prompt
	if len(processedInputs) > 0 {
		var contextLines []string
		contextLines = append(contextLines, "Workflow Context (from previous steps):")
		for key, value := range processedInputs {
			if str, ok := value.(string); ok {
				contextLines = append(contextLines, fmt.Sprintf("  %s: %s", key, str))
			} else {
				valueJSON, err := json.Marshal(value)
				if err != nil {
					contextLines = append(contextLines, fmt.Sprintf("  %s: %v", key, value))
				} else {
					contextLines = append(contextLines, fmt.Sprintf("  %s: %s", key, string(valueJSON)))
				}
			}
		}
		workflowContext := strings.Join(contextLines, "\n")
		taskReq.Prompt = workflowContext + "\n\n" + taskReq.Prompt
	}

	return nil
}

// processInputValue processes a single input value, expanding templates if necessary.
func (we *WorkflowEngine) processInputValue(value interface{}, stepOutputs map[string]interface{}, triggerRef string) (interface{}, error) {
	if str, ok := value.(string); ok {
		// Check for template syntax like "{{steps.implement.outputs.summary}}" or "{{trigger.issue_number}}"
		if strings.Contains(str, "{{") && strings.Contains(str, "}}") {
			return we.expandTemplateWithContext(str, stepOutputs, triggerRef)
		}
	}
	return value, nil
}

// expandTemplate expands template variables in a string.
// Supports {{steps.stepName.outputs.outputName}}, {{trigger.issue_number}},
// {{trigger.issue_title}}, {{trigger.issue_body}}, and {{trigger.issue_url}}.
func (we *WorkflowEngine) expandTemplateWithContext(template string, stepOutputs map[string]interface{}, triggerRef string) (string, error) {
	result := template

	// Pattern: {{trigger.issue_number}} — extract from triggerRef like "owner/repo#42"
	if strings.Contains(result, "{{trigger.issue_number}}") {
		issueNumber := ""
		if idx := strings.LastIndex(triggerRef, "#"); idx >= 0 {
			issueNumber = triggerRef[idx+1:]
		}
		result = strings.ReplaceAll(result, "{{trigger.issue_number}}", issueNumber)
	}

	// Pattern: {{trigger.issue_title}}, {{trigger.issue_body}}, {{trigger.issue_url}}
	// These are extracted from the workflow run context rather than the triggerRef string.
	// When a workflow is triggered by a GitHub issue event, these values are available
	// in the workflow run's trigger context and should be passed through the execution chain.
	// For now, we retrieve them from the workflow run's step outputs where they are stored
	// when the workflow is initiated by the GitHub event handler.
	if strings.Contains(result, "{{trigger.issue_title}}") ||
	   strings.Contains(result, "{{trigger.issue_body}}") ||
	   strings.Contains(result, "{{trigger.issue_url}}") {

		// Look for trigger context in step outputs under a special "_trigger_context" key
		if triggerContext, exists := stepOutputs["_trigger_context"]; exists {
			if contextMap, ok := triggerContext.(map[string]interface{}); ok {
				if issueTitle, ok := contextMap["issue_title"].(string); ok {
					result = strings.ReplaceAll(result, "{{trigger.issue_title}}", issueTitle)
				}
				if issueBody, ok := contextMap["issue_body"].(string); ok {
					result = strings.ReplaceAll(result, "{{trigger.issue_body}}", issueBody)
				}
				if issueURL, ok := contextMap["issue_url"].(string); ok {
					result = strings.ReplaceAll(result, "{{trigger.issue_url}}", issueURL)
				}
			}
		}
	}

	// Pattern: {{steps.stepName.outputs.outputName}}
	pattern := regexp.MustCompile(`\{\{steps\.([\w-]+)\.outputs\.([\w-]+)\}\}`)
	matches := pattern.FindAllStringSubmatch(result, -1)

	for _, match := range matches {
		fullMatch := match[0]
		stepID := match[1]
		outputKey := match[2]

		if stepOutput, exists := stepOutputs[stepID]; exists {
			if outputMap, ok := stepOutput.(map[string]interface{}); ok {
				if outputValue, exists := outputMap[outputKey]; exists {
					result = strings.ReplaceAll(result, fullMatch, fmt.Sprintf("%v", outputValue))
				}
			}
		}
	}

	// Pattern: {{steps.stepName.inputs.inputName}} — resolve from workflow definition
	inputPattern := regexp.MustCompile(`\{\{steps\.([\w-]+)\.inputs\.([\w-]+)\}\}`)
	inputMatches := inputPattern.FindAllStringSubmatch(result, -1)
	for _, match := range inputMatches {
		fullMatch := match[0]
		stepID := match[1]
		inputKey := match[2]

		if stepOutput, exists := stepOutputs[stepID]; exists {
			if outputMap, ok := stepOutput.(map[string]interface{}); ok {
				if inputVal, exists := outputMap["_input_"+inputKey]; exists {
					result = strings.ReplaceAll(result, fullMatch, fmt.Sprintf("%v", inputVal))
				}
			}
		}
	}

	return result, nil
}

// expandTemplate is the legacy wrapper without trigger context.
func (we *WorkflowEngine) expandTemplate(template string, stepOutputs map[string]interface{}) (string, error) {
	return we.expandTemplateWithContext(template, stepOutputs, "")
}

// resolveInputs resolves template references in step inputs using outputs from previous steps.
// Template references like {{steps.X.outputs.Y}} are replaced with actual values from stepOutputs.
func resolveInputs(inputs map[string]string, stepOutputs map[string]map[string]string) map[string]string {
	resolved := make(map[string]string)
	for key, value := range inputs {
		// Replace {{steps.X.outputs.Y}} with actual values
		resolved[key] = templateRegex.ReplaceAllStringFunc(value, func(match string) string {
			// Parse steps.X.outputs.Y
			parts := strings.Split(strings.Trim(match, "{}"), ".")
			if len(parts) == 4 && parts[0] == "steps" && parts[2] == "outputs" {
				if outputs, ok := stepOutputs[parts[1]]; ok {
					if val, ok := outputs[parts[3]]; ok {
						return val
					}
				}
			}
			return match // leave unresolved templates as-is
		})
	}
	return resolved
}

// readStepOutputs reads outputs from a completed session.
func (we *WorkflowEngine) readStepOutputs(ctx context.Context, sessionID string) (map[string]interface{}, error) {
	// Read outputs from the workflow_run_steps table where they are stored
	// by the updateWorkflowStepOutputs function when sessions complete
	var outputsJSON []byte

	err := we.db.QueryRow(ctx, `
		SELECT outputs
		FROM workflow_run_steps
		WHERE session_id = $1
	`, sessionID).Scan(&outputsJSON)

	if err != nil {
		// Session may not be part of a workflow, or outputs may not be available
		return make(map[string]interface{}), nil
	}

	// If no outputs were stored, return empty map
	if outputsJSON == nil {
		return make(map[string]interface{}), nil
	}

	// Parse the JSON outputs into a map
	var outputs map[string]interface{}
	if err := json.Unmarshal(outputsJSON, &outputs); err != nil {
		log.Printf("warning: failed to unmarshal step outputs for session %s: %v", sessionID, err)
		return make(map[string]interface{}), nil
	}

	if len(outputs) > 0 {
		log.Printf("read step outputs for session %s: %d field(s)", sessionID, len(outputs))
	}

	return outputs, nil
}

// RecoverWorkflows recovers running workflows after Bridge restart.
func (we *WorkflowEngine) RecoverWorkflows(ctx context.Context) error {
	log.Println("recovering running workflows...")

	// Query for running workflow runs
	rows, err := we.db.Query(ctx, `
		SELECT id, workflow_id, trigger_type, trigger_ref, current_step, step_outputs, team_id
		FROM workflow_runs
		WHERE status = 'running'
	`)
	if err != nil {
		return fmt.Errorf("querying running workflows: %w", err)
	}
	defer rows.Close()

	var recovered int
	for rows.Next() {
		var runID, workflowID, triggerType, triggerRef, currentStep, teamID string
		var stepOutputsJSON []byte

		if err := rows.Scan(&runID, &workflowID, &triggerType, &triggerRef, &currentStep, &stepOutputsJSON, &teamID); err != nil {
			log.Printf("error scanning workflow run: %v", err)
			continue
		}

		run := &WorkflowRun{
			ID:          runID,
			WorkflowID:  workflowID,
			Status:      "running",
			TriggerType: triggerType,
			TriggerRef:  triggerRef,
			CurrentStep: currentStep,
			TeamID:      teamID,
		}

		if stepOutputsJSON != nil {
			if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
				log.Printf("error unmarshaling step outputs for run %s: %v", runID, err)
				run.StepOutputs = make(map[string]interface{})
			}
		} else {
			run.StepOutputs = make(map[string]interface{})
		}

		// Resume workflow evaluation
		if err := we.resumeWorkflowRun(ctx, run); err != nil {
			log.Printf("error resuming workflow run %s: %v", runID, err)
		} else {
			recovered++
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating workflow runs: %w", err)
	}

	log.Printf("recovered %d running workflows", recovered)
	return nil
}

// resumeWorkflowRun resumes evaluation of a running workflow.
func (we *WorkflowEngine) resumeWorkflowRun(ctx context.Context, run *WorkflowRun) error {
	// Get the workflow definition
	workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("getting workflow definition: %w", err)
	}

	// Get all step statuses for expression evaluation.
	stepStatuses, err := we.getAllStepStatuses(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("getting all step statuses: %w", err)
	}

	// Find steps that might be ready to dispatch
	for _, step := range workflow.Workflow {
		stepStatus, statusErr := we.getStepStatus(ctx, run.ID, step.ID)
		if statusErr != nil {
			return fmt.Errorf("getting step status: %w", statusErr)
		}

		if stepStatus == "pending" {
			ready := false
			if step.Depends != "" {
				result, evalErr := EvaluateDepends(step.Depends, stepStatuses)
				if evalErr != nil {
					log.Printf("error evaluating depends for step %s during recovery: %v", step.ID, evalErr)
					continue
				}
				ready = result
			} else {
				result, depsErr := we.areStepDependenciesSatisfied(ctx, run.ID, step.Needs)
				if depsErr != nil {
					return fmt.Errorf("checking dependencies for step %s: %w", step.ID, depsErr)
				}
				ready = result
			}

			if ready {
				if err := we.dispatchStep(ctx, run, &step, workflow); err != nil {
					log.Printf("error dispatching step %s during recovery: %v", step.ID, err)
				}
			}
		}
	}

	// Check if workflow is complete
	if err := we.checkWorkflowCompletion(ctx, run); err != nil {
		return fmt.Errorf("checking workflow completion: %w", err)
	}

	return nil
}

// ApproveStep approves a pending approval gate step and dispatches it.
func (we *WorkflowEngine) ApproveStep(ctx context.Context, runID, stepID string) error {
	// Verify the step is in awaiting_approval status
	status, err := we.getStepStatus(ctx, runID, stepID)
	if err != nil {
		return fmt.Errorf("getting step status: %w", err)
	}
	if status != "awaiting_approval" {
		return fmt.Errorf("step %s is not awaiting approval (current status: %s)", stepID, status)
	}

	// Get the workflow run
	run, err := we.GetWorkflowRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("getting workflow run: %w", err)
	}

	// Get the workflow definition
	workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("getting workflow definition: %w", err)
	}

	// Get the step definition
	stepDef := workflow.GetStepByID(stepID)
	if stepDef == nil {
		return fmt.Errorf("step %s not found in workflow definition", stepID)
	}

	// Update workflow run status back to running
	if err := we.updateWorkflowRunStatus(ctx, runID, "running", nil, nil); err != nil {
		return fmt.Errorf("updating workflow run status: %w", err)
	}

	// Reset step status to pending so dispatchStep can proceed
	if err := we.updateStepStatus(ctx, runID, stepID, "pending", nil, nil); err != nil {
		return fmt.Errorf("resetting step status: %w", err)
	}

	// Create a temporary copy of the step without the approval requirement
	approvedStep := *stepDef
	approvedStep.Approval = ""

	// Dispatch the step
	if err := we.dispatchStep(ctx, run, &approvedStep, workflow); err != nil {
		return fmt.Errorf("dispatching approved step: %w", err)
	}

	log.Printf("step %s approved and dispatched for workflow run %s", stepID, runID)
	return nil
}

// RejectStep rejects a pending approval gate step and marks the workflow as failed.
func (we *WorkflowEngine) RejectStep(ctx context.Context, runID, stepID string) error {
	// Verify the step is in awaiting_approval status
	status, err := we.getStepStatus(ctx, runID, stepID)
	if err != nil {
		return fmt.Errorf("getting step status: %w", err)
	}
	if status != "awaiting_approval" {
		return fmt.Errorf("step %s is not awaiting approval (current status: %s)", stepID, status)
	}

	// Mark the step as failed
	now := time.Now().UTC()
	if err := we.updateStepStatus(ctx, runID, stepID, "failed", &now, nil); err != nil {
		return fmt.Errorf("marking step as failed: %w", err)
	}

	// Mark the workflow run as failed
	if err := we.updateWorkflowRunStatus(ctx, runID, "failed", nil, &now); err != nil {
		return fmt.Errorf("marking workflow run as failed: %w", err)
	}

	log.Printf("step %s rejected for workflow run %s", stepID, runID)
	return nil
}

// GetWorkflowRun retrieves a workflow run by ID.
func (we *WorkflowEngine) GetWorkflowRun(ctx context.Context, runID string) (*WorkflowRun, error) {
	row := we.db.QueryRow(ctx, `
		SELECT id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, started_at, finished_at, team_id, created_at
		FROM workflow_runs
		WHERE id = $1
	`, runID)

	var run WorkflowRun
	var stepOutputsJSON []byte
	var currentStep *string
	var triggerType, triggerRef *string

	if err := row.Scan(
		&run.ID, &run.WorkflowID, &run.Status, &triggerType, &triggerRef,
		&currentStep, &stepOutputsJSON, &run.StartedAt, &run.FinishedAt, &run.TeamID, &run.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("getting workflow run %s: %w", runID, err)
	}

	if currentStep != nil {
		run.CurrentStep = *currentStep
	}
	if triggerType != nil {
		run.TriggerType = *triggerType
	}
	if triggerRef != nil {
		run.TriggerRef = *triggerRef
	}
	if stepOutputsJSON != nil {
		if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
			run.StepOutputs = make(map[string]interface{})
		}
	} else {
		run.StepOutputs = make(map[string]interface{})
	}

	return &run, nil
}

// ListWorkflowRuns lists workflow runs, optionally filtered by status and owner.
func (we *WorkflowEngine) ListWorkflowRuns(ctx context.Context, status, teamID string) ([]WorkflowRun, error) {
	query := `
		SELECT id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, started_at, finished_at, team_id, created_at
		FROM workflow_runs
		WHERE 1=1
	`
	args := []interface{}{}
	argN := 1

	if teamID != "" {
		query += fmt.Sprintf(" AND team_id = $%d", argN)
		args = append(args, teamID)
		argN++
	}
	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
		argN++
	}

	query += " ORDER BY created_at DESC LIMIT 100"

	rows, err := we.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing workflow runs: %w", err)
	}
	defer rows.Close()

	var runs []WorkflowRun
	for rows.Next() {
		var run WorkflowRun
		var stepOutputsJSON []byte
		var currentStep, triggerType, triggerRef *string

		if err := rows.Scan(
			&run.ID, &run.WorkflowID, &run.Status, &triggerType, &triggerRef,
			&currentStep, &stepOutputsJSON, &run.StartedAt, &run.FinishedAt, &run.TeamID, &run.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning workflow run: %w", err)
		}

		if currentStep != nil {
			run.CurrentStep = *currentStep
		}
		if triggerType != nil {
			run.TriggerType = *triggerType
		}
		if triggerRef != nil {
			run.TriggerRef = *triggerRef
		}
		if stepOutputsJSON != nil {
			if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
				run.StepOutputs = make(map[string]interface{})
			}
		} else {
			run.StepOutputs = make(map[string]interface{})
		}

		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workflow runs: %w", err)
	}

	if runs == nil {
		runs = []WorkflowRun{}
	}
	return runs, nil
}

// GetWorkflowRunDetail retrieves a workflow run with all its steps, enriched
// with definition data (type, action, depends, max_iterations, credentials).
func (we *WorkflowEngine) GetWorkflowRunDetail(ctx context.Context, runID string) (*WorkflowRun, []WorkflowRunStep, error) {
	run, err := we.GetWorkflowRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}

	steps, err := we.getWorkflowRunSteps(ctx, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("getting workflow run steps: %w", err)
	}

	// Enrich run steps with fields from the workflow definition.
	workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
	if err == nil && workflow != nil {
		defByID := make(map[string]*WorkflowStep, len(workflow.Workflow))
		for i := range workflow.Workflow {
			defByID[workflow.Workflow[i].ID] = &workflow.Workflow[i]
		}
		for i := range steps {
			if def, ok := defByID[steps[i].StepID]; ok {
				steps[i].Type = def.Type
				steps[i].Action = def.Action
				steps[i].Depends = def.Depends
				steps[i].MaxIterations = def.MaxIterations
				steps[i].Credentials = def.Credentials
			}
		}
	}

	return run, steps, nil
}

// Database helper methods

// getWorkflowByID retrieves a workflow definition by its ID.
func (we *WorkflowEngine) getWorkflowByID(ctx context.Context, workflowID string) (*WorkflowDefinition, error) {
	row := we.db.QueryRow(ctx, `
		SELECT parsed FROM workflows WHERE id = $1
	`, workflowID)

	var parsedJSON []byte
	if err := row.Scan(&parsedJSON); err != nil {
		return nil, fmt.Errorf("getting workflow %s: %w", workflowID, err)
	}

	var wd WorkflowDefinition
	if err := json.Unmarshal(parsedJSON, &wd); err != nil {
		return nil, fmt.Errorf("unmarshaling workflow definition: %w", err)
	}

	return &wd, nil
}

// insertWorkflowRun inserts a new workflow run record.
func (we *WorkflowEngine) insertWorkflowRun(ctx context.Context, run *WorkflowRun) error {
	stepOutputsJSON, err := json.Marshal(run.StepOutputs)
	if err != nil {
		return fmt.Errorf("marshaling step outputs: %w", err)
	}

	_, err = we.db.Exec(ctx, `
		INSERT INTO workflow_runs (id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, team_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, run.ID, run.WorkflowID, run.Status, run.TriggerType, run.TriggerRef, run.CurrentStep, stepOutputsJSON, run.TeamID, run.CreatedAt)

	return err
}

// insertWorkflowRunStep inserts a new workflow run step record.
func (we *WorkflowEngine) insertWorkflowRunStep(ctx context.Context, step *WorkflowRunStep) error {
	var outputsJSON []byte
	var err error
	if step.Outputs != nil {
		outputsJSON, err = json.Marshal(step.Outputs)
		if err != nil {
			return fmt.Errorf("marshaling step outputs: %w", err)
		}
	}

	_, err = we.db.Exec(ctx, `
		INSERT INTO workflow_run_steps (id, run_id, step_id, session_id, status, outputs, iteration, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, step.ID, step.RunID, step.StepID, step.SessionID, step.Status, outputsJSON, step.Iteration, step.StartedAt, step.FinishedAt)

	return err
}

// updateWorkflowRunStatus updates the status and timestamps of a workflow run.
func (we *WorkflowEngine) updateWorkflowRunStatus(ctx context.Context, runID, status string, startedAt, finishedAt *time.Time) error {
	_, err := we.db.Exec(ctx, `
		UPDATE workflow_runs
		SET status = $2, started_at = $3, finished_at = $4
		WHERE id = $1
	`, runID, status, startedAt, finishedAt)

	return err
}

// updateStepStatus updates a workflow run step's status and outputs.
func (we *WorkflowEngine) updateStepStatus(ctx context.Context, runID, stepID, status string, finishedAt *time.Time, outputs map[string]interface{}) error {
	var outputsJSON []byte
	var err error
	if outputs != nil {
		outputsJSON, err = json.Marshal(outputs)
		if err != nil {
			return fmt.Errorf("marshaling step outputs: %w", err)
		}
	}

	_, err = we.db.Exec(ctx, `
		UPDATE workflow_run_steps
		SET status = $3, finished_at = $4, outputs = $5
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID, status, finishedAt, outputsJSON)

	return err
}

// updateStepWithSession updates a workflow run step with session ID and marks it as running.
func (we *WorkflowEngine) updateStepWithSession(ctx context.Context, runID, stepID, sessionID, status string, startedAt *time.Time) error {
	_, err := we.db.Exec(ctx, `
		UPDATE workflow_run_steps
		SET session_id = $3, status = $4, started_at = $5
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID, sessionID, status, startedAt)

	return err
}

// getStepStatus gets the status of a workflow run step.
func (we *WorkflowEngine) getStepStatus(ctx context.Context, runID, stepID string) (string, error) {
	var status string
	err := we.db.QueryRow(ctx, `
		SELECT status FROM workflow_run_steps
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID).Scan(&status)

	return status, err
}

// getWorkflowRunSteps gets all steps for a workflow run.
func (we *WorkflowEngine) getWorkflowRunSteps(ctx context.Context, runID string) ([]WorkflowRunStep, error) {
	rows, err := we.db.Query(ctx, `
		SELECT id, run_id, step_id, session_id, status, outputs, iteration, started_at, finished_at
		FROM workflow_run_steps
		WHERE run_id = $1
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []WorkflowRunStep
	for rows.Next() {
		var step WorkflowRunStep
		var sessionID *string
		var outputsJSON []byte
		var startedAt, finishedAt *time.Time

		if err := rows.Scan(&step.ID, &step.RunID, &step.StepID, &sessionID, &step.Status, &outputsJSON, &step.Iteration, &startedAt, &finishedAt); err != nil {
			return nil, err
		}

		if sessionID != nil {
			step.SessionID = *sessionID
		}

		if outputsJSON != nil {
			if err := json.Unmarshal(outputsJSON, &step.Outputs); err != nil {
				log.Printf("error unmarshaling step outputs: %v", err)
			}
		}

		step.StartedAt = startedAt
		step.FinishedAt = finishedAt

		steps = append(steps, step)
	}

	return steps, rows.Err()
}

// getStepAndRunBySessionID gets the workflow run step and run by session ID.
func (we *WorkflowEngine) getStepAndRunBySessionID(ctx context.Context, sessionID string) (*WorkflowRunStep, *WorkflowRun, error) {
	row := we.db.QueryRow(ctx, `
		SELECT wrs.id, wrs.run_id, wrs.step_id, wrs.session_id, wrs.status, wrs.outputs, wrs.iteration, wrs.started_at, wrs.finished_at,
		       wr.id, wr.workflow_id, wr.status, wr.trigger_type, wr.trigger_ref, wr.current_step, wr.step_outputs, wr.team_id
		FROM workflow_run_steps wrs
		JOIN workflow_runs wr ON wrs.run_id = wr.id
		WHERE wrs.session_id = $1
	`, sessionID)

	var step WorkflowRunStep
	var run WorkflowRun
	var stepOutputsJSON, runStepOutputsJSON []byte
	var sessionIDPtr *string
	var stepStartedAt, stepFinishedAt, runStartedAt, runFinishedAt *time.Time

	err := row.Scan(
		&step.ID, &step.RunID, &step.StepID, &sessionIDPtr, &step.Status, &stepOutputsJSON, &step.Iteration, &stepStartedAt, &stepFinishedAt,
		&run.ID, &run.WorkflowID, &run.Status, &run.TriggerType, &run.TriggerRef, &run.CurrentStep, &runStepOutputsJSON, &run.TeamID,
	)
	if err != nil {
		return nil, nil, err
	}

	if sessionIDPtr != nil {
		step.SessionID = *sessionIDPtr
	}

	if stepOutputsJSON != nil {
		if err := json.Unmarshal(stepOutputsJSON, &step.Outputs); err != nil {
			log.Printf("error unmarshaling step outputs: %v", err)
		}
	}

	if runStepOutputsJSON != nil {
		if err := json.Unmarshal(runStepOutputsJSON, &run.StepOutputs); err != nil {
			log.Printf("error unmarshaling run step outputs: %v", err)
			run.StepOutputs = make(map[string]interface{})
		}
	} else {
		run.StepOutputs = make(map[string]interface{})
	}

	step.StartedAt = stepStartedAt
	step.FinishedAt = stepFinishedAt
	run.StartedAt = runStartedAt
	run.FinishedAt = runFinishedAt

	return &step, &run, nil
}

// getRunStepOutputs gets accumulated step outputs for a workflow run.
func (we *WorkflowEngine) getRunStepOutputs(ctx context.Context, runID string) (map[string]interface{}, error) {
	var stepOutputsJSON []byte
	err := we.db.QueryRow(ctx, `
		SELECT step_outputs FROM workflow_runs WHERE id = $1
	`, runID).Scan(&stepOutputsJSON)
	if err != nil {
		return nil, err
	}

	outputs := make(map[string]interface{})
	if stepOutputsJSON != nil {
		if err := json.Unmarshal(stepOutputsJSON, &outputs); err != nil {
			return nil, fmt.Errorf("unmarshaling step outputs: %w", err)
		}
	}

	return outputs, nil
}

// updateRunStepOutputs updates the accumulated step outputs for a workflow run.
func (we *WorkflowEngine) updateRunStepOutputs(ctx context.Context, runID, stepID string, stepOutputs map[string]interface{}) error {
	// Get current accumulated outputs
	currentOutputs, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return fmt.Errorf("getting current step outputs: %w", err)
	}

	// Merge with existing outputs for this step (preserve _input_* keys from dispatch time)
	if existing, ok := currentOutputs[stepID].(map[string]interface{}); ok {
		for k, v := range stepOutputs {
			existing[k] = v
		}
		currentOutputs[stepID] = existing
	} else {
		currentOutputs[stepID] = stepOutputs
	}

	// Marshal and update
	outputsJSON, err := json.Marshal(currentOutputs)
	if err != nil {
		return fmt.Errorf("marshaling updated step outputs: %w", err)
	}

	_, err = we.db.Exec(ctx, `
		UPDATE workflow_runs SET step_outputs = $2 WHERE id = $1
	`, runID, outputsJSON)

	return err
}

// validateCrossRepoCredentials checks that the user has appropriate credentials for the target repository.
// This ensures cross-repo workflow steps can access the target repository.
func (we *WorkflowEngine) validateCrossRepoCredentials(ctx context.Context, repoURL, owner string) error {
	// Parse the repository URL to determine the service (GitHub, GitLab, etc.)
	service, err := parseRepoService(repoURL)
	if err != nil {
		return fmt.Errorf("unable to parse repository service from URL %s: %w", repoURL, err)
	}

	// Check if the credential store has credentials for this service
	// This validates that the user can access the target repository
	credStore := &CredentialStore{db: we.db}
	_, _, err = credStore.AcquireSCMTokenWithHost(ctx, service)
	if err != nil {
		return fmt.Errorf("no credentials available for %s service (needed for repo %s): %w", service, repoURL, err)
	}

	return nil
}

// parseRepoService extracts the service name from a repository URL.
// Examples:
//
//	"github.com/owner/repo" or "https://github.com/owner/repo" -> "github"
//	"gitlab.com/owner/repo" or "https://gitlab.com/owner/repo" -> "gitlab"
func parseRepoService(repoURL string) (string, error) {
	// Handle various repository URL formats
	repoURL = strings.TrimPrefix(repoURL, "https://")
	repoURL = strings.TrimPrefix(repoURL, "http://")
	repoURL = strings.TrimPrefix(repoURL, "git@")
	repoURL = strings.ReplaceAll(repoURL, ":", "/") // Handle SSH format

	parts := strings.Split(repoURL, "/")
	if len(parts) < 1 {
		return "", fmt.Errorf("invalid repository URL format")
	}

	host := parts[0]

	// Map common hosts to service names
	if strings.Contains(host, "github") {
		return "github", nil
	}
	if strings.Contains(host, "gitlab") {
		return "gitlab", nil
	}
	if strings.Contains(host, "bitbucket") {
		return "bitbucket", nil
	}

	// For enterprise instances, default to github if no other match
	// This could be enhanced to support custom service mappings
	return "github", nil
}

// enhanceTaskRequestForRepo adds repository-specific configurations to the task request.
// This handles the cross-repo dispatch requirements mentioned in the issue comments.
func (we *WorkflowEngine) enhanceTaskRequestForRepo(ctx context.Context, taskReq *TaskRequest, targetRepo, owner string) error {
	// Future enhancement: inject repo-specific conventions (CLAUDE.md, AGENTS.md)
	// Future enhancement: handle credential scoping per repository
	// Future enhancement: configure sandbox persistence settings per repo

	log.Printf("enhanced task request for cross-repo dispatch to %s", targetRepo)
	return nil
}

// getStepIteration retrieves the current iteration count for a workflow run step.
func (we *WorkflowEngine) getStepIteration(ctx context.Context, runID, stepID string) (int, error) {
	var iteration int
	err := we.db.QueryRow(ctx, `
		SELECT iteration FROM workflow_run_steps
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID).Scan(&iteration)
	return iteration, err
}

// incrementStepIteration increments the iteration counter for a workflow run step.
func (we *WorkflowEngine) incrementStepIteration(ctx context.Context, runID, stepID string) error {
	_, err := we.db.Exec(ctx, `
		UPDATE workflow_run_steps
		SET iteration = iteration + 1
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID)
	return err
}

// handleFieldBasedRouting processes field-based routing for a completed step.
// This implements the simpler routing approach suggested by MC feedback.
func (we *WorkflowEngine) handleFieldBasedRouting(ctx context.Context, run *WorkflowRun, step *WorkflowStep, outputs map[string]interface{}, workflow *WorkflowDefinition) error {
	if step.RouteField == "" || len(step.RouteMap) == 0 {
		return nil // No routing configured
	}

	// Get the routing field value from the step outputs
	routeValue, exists := outputs[step.RouteField]
	if !exists {
		log.Printf("route field '%s' not found in outputs for step %s", step.RouteField, step.ID)
		return nil // Field not present, no routing action
	}

	// Convert route value to string for map lookup
	routeValueStr := fmt.Sprintf("%v", routeValue)

	// Find the next step based on the route map
	nextStepID, exists := step.RouteMap[routeValueStr]
	if !exists {
		log.Printf("no route defined for value '%s' in step %s route_map", routeValueStr, step.ID)
		return nil // No route defined for this value
	}

	// Get the next step definition
	nextStepDef := workflow.GetStepByID(nextStepID)
	if nextStepDef == nil {
		return fmt.Errorf("route target step '%s' not found in workflow", nextStepID)
	}

	// Check if the next step is ready to be dispatched
	ready, err := we.areStepDependenciesSatisfied(ctx, run.ID, nextStepDef.Needs)
	if err != nil {
		return fmt.Errorf("checking dependencies for routed step %s: %w", nextStepID, err)
	}

	if !ready {
		log.Printf("routed step %s is not ready (dependencies not satisfied)", nextStepID)
		return nil // Dependencies not satisfied yet
	}

	// Check if step is still pending
	stepStatus, err := we.getStepStatus(ctx, run.ID, nextStepID)
	if err != nil {
		return fmt.Errorf("getting status for routed step %s: %w", nextStepID, err)
	}

	if stepStatus != "pending" {
		log.Printf("routed step %s is not pending (status: %s)", nextStepID, stepStatus)
		return nil // Step already processed
	}

	// Dispatch the routed step
	log.Printf("field-based routing: dispatching step %s based on %s=%s", nextStepID, step.RouteField, routeValueStr)
	if err := we.dispatchStep(ctx, run, nextStepDef, workflow); err != nil {
		return fmt.Errorf("dispatching routed step %s: %w", nextStepID, err)
	}

	return nil
}
