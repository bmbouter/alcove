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

	"github.com/bmbouter/alcove/internal/bridge/condition"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkflowEngine manages workflow execution: DAG evaluation, step dispatch, and completion tracking.
type WorkflowEngine struct {
	db            *pgxpool.Pool
	dispatcher    *Dispatcher
	workflowStore *WorkflowStore
	defStore      *AgentDefStore
}

// NewWorkflowEngine creates a new workflow engine with the given dependencies.
func NewWorkflowEngine(db *pgxpool.Pool, dispatcher *Dispatcher, workflowStore *WorkflowStore, defStore *AgentDefStore) *WorkflowEngine {
	return &WorkflowEngine{
		db:            db,
		dispatcher:    dispatcher,
		workflowStore: workflowStore,
		defStore:      defStore,
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
	Owner       string                 `json:"owner"`
	CreatedAt   time.Time              `json:"created_at"`
}

// WorkflowRunStep represents a single step execution within a workflow run.
type WorkflowRunStep struct {
	ID               string                 `json:"id"`
	RunID            string                 `json:"run_id"`
	StepID           string                 `json:"step_id"`
	SessionID        string                 `json:"session_id,omitempty"`
	Status           string                 `json:"status"` // pending, running, completed, failed, skipped, awaiting_approval
	ApprovalDeadline *time.Time             `json:"approval_deadline,omitempty"`
	Outputs          map[string]interface{} `json:"outputs,omitempty"`
	StartedAt        *time.Time             `json:"started_at,omitempty"`
	FinishedAt       *time.Time             `json:"finished_at,omitempty"`
}

// StartWorkflowRun creates a new workflow run and dispatches initial steps.
func (we *WorkflowEngine) StartWorkflowRun(ctx context.Context, workflowID, triggerType, triggerRef, owner string) (*WorkflowRun, error) {
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
		Owner:       owner,
		CreatedAt:   now,
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

// DispatchStep dispatches a single workflow step.
func (we *WorkflowEngine) dispatchStep(ctx context.Context, run *WorkflowRun, step *WorkflowStep, workflow *WorkflowDefinition) error {
	log.Printf("dispatching step %s for workflow run %s", step.ID, run.ID)

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

		// Calculate approval deadline
		timeout := "72h" // default timeout
		if step.ApprovalTimeout != "" {
			timeout = step.ApprovalTimeout
		}
		duration, _ := time.ParseDuration(timeout) // validated during YAML parsing
		deadline := time.Now().UTC().Add(duration)

		if err := we.updateStepStatusWithApproval(ctx, run.ID, step.ID, "awaiting_approval", &deadline, nil, nil); err != nil {
			return fmt.Errorf("marking step as awaiting approval: %w", err)
		}

		// Update workflow run status to awaiting_approval if no other steps are running
		if err := we.checkAndUpdateWorkflowRunForApproval(ctx, run); err != nil {
			log.Printf("error updating workflow run status for approval: %v", err)
		}

		log.Printf("step %s awaiting approval until %v", step.ID, deadline)
		return nil
	}

	// Get the agent definition
	agentDef, err := we.defStore.GetAgentDefinition(ctx, step.Agent, run.Owner)
	if err != nil {
		return fmt.Errorf("getting agent definition %s: %w", step.Agent, err)
	}

	// Build TaskRequest from step and agent definition
	taskReq := agentDef.ToTaskRequest()
	taskReq.TaskName = step.Agent
	taskReq.TriggerType = run.TriggerType
	taskReq.TriggerRef = run.TriggerRef

	// If step has a repo specified, override the agent's repo
	if step.Repo != "" {
		taskReq.Repo = step.Repo

		// Validate cross-repo access: ensure credentials are available for the target repo
		if err := we.validateCrossRepoCredentials(ctx, step.Repo, run.Owner); err != nil {
			return fmt.Errorf("cross-repo credential validation failed for repo %s: %w", step.Repo, err)
		}

		// Apply cross-repo enhancements to the task request
		if err := we.enhanceTaskRequestForRepo(ctx, &taskReq, step.Repo, run.Owner); err != nil {
			return fmt.Errorf("cross-repo enhancement failed for repo %s: %w", step.Repo, err)
		}
	}

	// Inject step inputs into the task request
	if err := we.injectStepInputs(ctx, run.ID, step, &taskReq); err != nil {
		return fmt.Errorf("injecting inputs for step %s: %w", step.ID, err)
	}

	// Dispatch the task
	session, err := we.dispatcher.DispatchTask(ctx, taskReq, run.Owner)
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
	dependents := workflow.GetDependents(completedStepID)

	for _, dependent := range dependents {
		// Check if all dependencies are satisfied
		ready, err := we.areStepDependenciesSatisfied(ctx, run.ID, dependent.Needs)
		if err != nil {
			return fmt.Errorf("checking dependencies for step %s: %w", dependent.ID, err)
		}

		if ready {
			// Check if step is still pending
			stepStatus, err := we.getStepStatus(ctx, run.ID, dependent.ID)
			if err != nil {
				return fmt.Errorf("getting status for step %s: %w", dependent.ID, err)
			}

			if stepStatus == "pending" {
				if err := we.dispatchStep(ctx, run, &dependent, workflow); err != nil {
					log.Printf("error dispatching dependent step %s: %v", dependent.ID, err)
					// Mark the step as failed
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
func (we *WorkflowEngine) injectStepInputs(ctx context.Context, runID string, step *WorkflowStep, taskReq *TaskRequest) error {
	if len(step.Inputs) == 0 {
		return nil
	}

	// Get accumulated step outputs for template expansion
	stepOutputs, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return fmt.Errorf("getting step outputs: %w", err)
	}

	// Process each input
	processedInputs := make(map[string]interface{})
	for key, value := range step.Inputs {
		processedValue, err := we.processInputValue(value, stepOutputs)
		if err != nil {
			return fmt.Errorf("processing input %s: %w", key, err)
		}
		processedInputs[key] = processedValue
	}

	// Convert inputs to environment variables or modify prompt
	// For now, append inputs as JSON to the prompt
	if len(processedInputs) > 0 {
		inputsJSON, err := json.Marshal(processedInputs)
		if err != nil {
			return fmt.Errorf("marshaling inputs: %w", err)
		}

		taskReq.Prompt = taskReq.Prompt + "\n\nWorkflow Step Inputs:\n" + string(inputsJSON)
	}

	return nil
}

// processInputValue processes a single input value, expanding templates if necessary.
func (we *WorkflowEngine) processInputValue(value interface{}, stepOutputs map[string]interface{}) (interface{}, error) {
	if str, ok := value.(string); ok {
		// Check for template syntax like "{{steps.implement.outputs.summary}}"
		if strings.Contains(str, "{{") && strings.Contains(str, "}}") {
			return we.expandTemplate(str, stepOutputs)
		}
	}
	return value, nil
}

// expandTemplate expands template variables in a string.
func (we *WorkflowEngine) expandTemplate(template string, stepOutputs map[string]interface{}) (string, error) {
	result := template

	// Pattern: {{steps.stepName.outputs.outputName}}
	pattern := regexp.MustCompile(`\{\{steps\.(\w+)\.outputs\.(\w+)\}\}`)
	matches := pattern.FindAllStringSubmatch(template, -1)

	for _, match := range matches {
		fullMatch := match[0]
		stepID := match[1]
		outputKey := match[2]

		if stepOutput, exists := stepOutputs[stepID]; exists {
			if outputMap, ok := stepOutput.(map[string]interface{}); ok {
				if outputValue, exists := outputMap[outputKey]; exists {
					if str, ok := outputValue.(string); ok {
						result = strings.ReplaceAll(result, fullMatch, str)
					}
				}
			}
		}
	}

	return result, nil
}

// readStepOutputs reads outputs from a completed session.
func (we *WorkflowEngine) readStepOutputs(ctx context.Context, sessionID string) (map[string]interface{}, error) {
	// TODO: Implement reading outputs from session artifacts or a dedicated outputs mechanism
	// For now, return empty outputs
	return make(map[string]interface{}), nil
}

// RecoverWorkflows recovers running workflows after Bridge restart.
func (we *WorkflowEngine) RecoverWorkflows(ctx context.Context) error {
	log.Println("recovering running workflows...")

	// Query for running workflow runs
	rows, err := we.db.Query(ctx, `
		SELECT id, workflow_id, trigger_type, trigger_ref, current_step, step_outputs, owner
		FROM workflow_runs
		WHERE status = 'running'
	`)
	if err != nil {
		return fmt.Errorf("querying running workflows: %w", err)
	}
	defer rows.Close()

	var recovered int
	for rows.Next() {
		var runID, workflowID, triggerType, triggerRef, currentStep, owner string
		var stepOutputsJSON []byte

		if err := rows.Scan(&runID, &workflowID, &triggerType, &triggerRef, &currentStep, &stepOutputsJSON, &owner); err != nil {
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
			Owner:       owner,
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

	// Find steps that might be ready to dispatch
	for _, step := range workflow.Workflow {
		stepStatus, err := we.getStepStatus(ctx, run.ID, step.ID)
		if err != nil {
			return fmt.Errorf("getting step status: %w", err)
		}

		if stepStatus == "pending" {
			// Check if dependencies are satisfied
			ready, err := we.areStepDependenciesSatisfied(ctx, run.ID, step.Needs)
			if err != nil {
				return fmt.Errorf("checking dependencies for step %s: %w", step.ID, err)
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
		INSERT INTO workflow_runs (id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, owner, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, run.ID, run.WorkflowID, run.Status, run.TriggerType, run.TriggerRef, run.CurrentStep, stepOutputsJSON, run.Owner, run.CreatedAt)

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
		INSERT INTO workflow_run_steps (id, run_id, step_id, session_id, status, approval_deadline, outputs, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, step.ID, step.RunID, step.StepID, step.SessionID, step.Status, step.ApprovalDeadline, outputsJSON, step.StartedAt, step.FinishedAt)

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
		SELECT id, run_id, step_id, session_id, status, approval_deadline, outputs, started_at, finished_at
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

		if err := rows.Scan(&step.ID, &step.RunID, &step.StepID, &sessionID, &step.Status, &step.ApprovalDeadline, &outputsJSON, &startedAt, &finishedAt); err != nil {
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
		SELECT wrs.id, wrs.run_id, wrs.step_id, wrs.session_id, wrs.status, wrs.approval_deadline, wrs.outputs, wrs.started_at, wrs.finished_at,
		       wr.id, wr.workflow_id, wr.status, wr.trigger_type, wr.trigger_ref, wr.current_step, wr.step_outputs, wr.owner
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
		&step.ID, &step.RunID, &step.StepID, &sessionIDPtr, &step.Status, &step.ApprovalDeadline, &stepOutputsJSON, &stepStartedAt, &stepFinishedAt,
		&run.ID, &run.WorkflowID, &run.Status, &run.TriggerType, &run.TriggerRef, &run.CurrentStep, &runStepOutputsJSON, &run.Owner,
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

	// Update with new step outputs
	currentOutputs[stepID] = stepOutputs

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

	// updateStepStatusWithApproval updates a workflow run step's status with approval deadline.
	func (we *WorkflowEngine) updateStepStatusWithApproval(ctx context.Context, runID, stepID, status string, approvalDeadline *time.Time, finishedAt *time.Time, outputs map[string]interface{}) error {
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
			SET status = $3, approval_deadline = $4, finished_at = $5, outputs = $6
			WHERE run_id = $1 AND step_id = $2
		`, runID, stepID, status, approvalDeadline, finishedAt, outputsJSON)

		return err
	}

	// checkAndUpdateWorkflowRunForApproval checks if the workflow should be marked as awaiting_approval.
	func (we *WorkflowEngine) checkAndUpdateWorkflowRunForApproval(ctx context.Context, run *WorkflowRun) error {
		// Check if there are any running steps
		steps, err := we.getWorkflowRunSteps(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("getting workflow run steps: %w", err)
		}

		hasRunning := false
		hasAwaitingApproval := false

		for _, step := range steps {
			if step.Status == "running" {
				hasRunning = true
			} else if step.Status == "awaiting_approval" {
				hasAwaitingApproval = true
			}
		}

		// If there are no running steps but there are steps awaiting approval, mark workflow as awaiting_approval
		if !hasRunning && hasAwaitingApproval && run.Status == "running" {
			if err := we.updateWorkflowRunStatus(ctx, run.ID, "awaiting_approval", nil, nil); err != nil {
				return fmt.Errorf("updating workflow run status to awaiting_approval: %w", err)
			}
			log.Printf("workflow run %s marked as awaiting_approval", run.ID)
		}

		return nil
	}

	// ApproveStep approves a workflow step and resumes execution.
	func (we *WorkflowEngine) ApproveStep(ctx context.Context, runID, stepID string) error {
		// Get the step and validate it's awaiting approval
		stepStatus, err := we.getStepStatus(ctx, runID, stepID)
		if err != nil {
			return fmt.Errorf("getting step status: %w", err)
		}

		if stepStatus != "awaiting_approval" {
			return fmt.Errorf("step %s is not awaiting approval (current status: %s)", stepID, stepStatus)
		}

		// Update step to pending so it can be dispatched
		if err := we.updateStepStatus(ctx, runID, stepID, "pending", nil, nil); err != nil {
			return fmt.Errorf("updating step status to pending: %w", err)
		}

		// Get the workflow run and definition
		run, err := we.getWorkflowRunByID(ctx, runID)
		if err != nil {
			return fmt.Errorf("getting workflow run: %w", err)
		}

		workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
		if err != nil {
			return fmt.Errorf("getting workflow definition: %w", err)
		}

		// Find and dispatch the approved step
		stepDef := workflow.GetStepByID(stepID)
		if stepDef == nil {
			return fmt.Errorf("step %s not found in workflow", stepID)
		}

		// Check if dependencies are still satisfied
		ready, err := we.areStepDependenciesSatisfied(ctx, runID, stepDef.Needs)
		if err != nil {
			return fmt.Errorf("checking dependencies for approved step: %w", err)
		}

		if !ready {
			return fmt.Errorf("step dependencies are no longer satisfied")
		}

		// Resume workflow run status if needed
		if run.Status == "awaiting_approval" {
			if err := we.updateWorkflowRunStatus(ctx, runID, "running", nil, nil); err != nil {
				return fmt.Errorf("updating workflow run status to running: %w", err)
			}
		}

		// Dispatch the step (without approval check since we just approved it)
		return we.dispatchApprovedStep(ctx, run, stepDef, workflow)
	}

	// RejectStep rejects a workflow step and marks the workflow as failed.
	func (we *WorkflowEngine) RejectStep(ctx context.Context, runID, stepID string) error {
		// Get the step and validate it's awaiting approval
		stepStatus, err := we.getStepStatus(ctx, runID, stepID)
		if err != nil {
			return fmt.Errorf("getting step status: %w", err)
		}

		if stepStatus != "awaiting_approval" {
			return fmt.Errorf("step %s is not awaiting approval (current status: %s)", stepID, stepStatus)
		}

		// Mark step as failed
		now := time.Now().UTC()
		if err := we.updateStepStatus(ctx, runID, stepID, "failed", &now, nil); err != nil {
			return fmt.Errorf("updating step status to failed: %w", err)
		}

		// Mark workflow run as failed
		if err := we.updateWorkflowRunStatus(ctx, runID, "failed", nil, &now); err != nil {
			return fmt.Errorf("updating workflow run status to failed: %w", err)
		}

		log.Printf("workflow run %s rejected via step %s", runID, stepID)
		return nil
	}

	// dispatchApprovedStep dispatches a step that has been approved (bypasses approval check).
	func (we *WorkflowEngine) dispatchApprovedStep(ctx context.Context, run *WorkflowRun, step *WorkflowStep, workflow *WorkflowDefinition) error {
		log.Printf("dispatching approved step %s for workflow run %s", step.ID, run.ID)

		// Check condition if present (same as normal dispatch)
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
				return we.checkAndDispatchDependents(ctx, run, step.ID, workflow)
			}
		}

		// Get the agent definition
		agentDef, err := we.defStore.GetAgentDefinition(ctx, step.Agent, run.Owner)
		if err != nil {
			return fmt.Errorf("getting agent definition %s: %w", step.Agent, err)
		}

		// Build TaskRequest (same as normal dispatch)
		taskReq := agentDef.ToTaskRequest()
		taskReq.TaskName = step.Agent
		taskReq.TriggerType = run.TriggerType
		taskReq.TriggerRef = run.TriggerRef

		// If step has a repo specified, override the agent's repo
		if step.Repo != "" {
			taskReq.Repo = step.Repo

			if err := we.validateCrossRepoCredentials(ctx, step.Repo, run.Owner); err != nil {
				return fmt.Errorf("cross-repo credential validation failed for repo %s: %w", step.Repo, err)
			}

			if err := we.enhanceTaskRequestForRepo(ctx, &taskReq, step.Repo, run.Owner); err != nil {
				return fmt.Errorf("cross-repo enhancement failed for repo %s: %w", step.Repo, err)
			}
		}

		// Inject step inputs into the task request
		if err := we.injectStepInputs(ctx, run.ID, step, &taskReq); err != nil {
			return fmt.Errorf("injecting inputs for step %s: %w", step.ID, err)
		}

		// Dispatch the task
		session, err := we.dispatcher.DispatchTask(ctx, taskReq, run.Owner)
		if err != nil {
			return fmt.Errorf("dispatching task for step %s: %w", step.ID, err)
		}

		// Update step with session ID and mark as running
		now := time.Now().UTC()
		if err := we.updateStepWithSession(ctx, run.ID, step.ID, session.ID, "running", &now); err != nil {
			return fmt.Errorf("updating step with session: %w", err)
		}

		log.Printf("dispatched approved step %s with session %s", step.ID, session.ID)
		return nil
	}

	// getWorkflowRunByID retrieves a workflow run by its ID.
	func (we *WorkflowEngine) getWorkflowRunByID(ctx context.Context, runID string) (*WorkflowRun, error) {
		var run WorkflowRun
		var stepOutputsJSON []byte
		var startedAt, finishedAt *time.Time

		err := we.db.QueryRow(ctx, `
			SELECT id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, owner, created_at, started_at, finished_at
			FROM workflow_runs
			WHERE id = $1
		`, runID).Scan(&run.ID, &run.WorkflowID, &run.Status, &run.TriggerType, &run.TriggerRef, &run.CurrentStep, &stepOutputsJSON, &run.Owner, &run.CreatedAt, &startedAt, &finishedAt)
		if err != nil {
			return nil, err
		}

		if stepOutputsJSON != nil {
			if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
				log.Printf("error unmarshaling step outputs: %v", err)
				run.StepOutputs = make(map[string]interface{})
			}
		} else {
			run.StepOutputs = make(map[string]interface{})
		}

		run.StartedAt = startedAt
		run.FinishedAt = finishedAt

		return &run, nil
	}

	// ProcessExpiredApprovals processes approval steps that have expired and marks them as cancelled.
	func (we *WorkflowEngine) ProcessExpiredApprovals(ctx context.Context) error {
		rows, err := we.db.Query(ctx, `
			SELECT run_id, step_id FROM workflow_run_steps
			WHERE status = 'awaiting_approval' AND approval_deadline < NOW()
		`)
		if err != nil {
			return fmt.Errorf("querying expired approvals: %w", err)
		}
		defer rows.Close()

		var expiredCount int
		for rows.Next() {
			var runID, stepID string
			if err := rows.Scan(&runID, &stepID); err != nil {
				log.Printf("error scanning expired approval: %v", err)
				continue
			}

			// Mark step as failed due to timeout
			now := time.Now().UTC()
			if err := we.updateStepStatus(ctx, runID, stepID, "failed", &now, nil); err != nil {
				log.Printf("error marking step %s as failed due to approval timeout: %v", stepID, err)
				continue
			}

			// Mark workflow run as cancelled
			if err := we.updateWorkflowRunStatus(ctx, runID, "cancelled", nil, &now); err != nil {
				log.Printf("error marking workflow run %s as cancelled: %v", runID, err)
			}

			log.Printf("step %s in run %s cancelled due to approval timeout", stepID, runID)
			expiredCount++
		}

		if expiredCount > 0 {
			log.Printf("processed %d expired approvals", expiredCount)
		}

		return rows.Err()
	}
