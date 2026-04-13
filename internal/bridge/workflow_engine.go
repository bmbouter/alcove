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
	ID              string                 `json:"id"`
	RunID           string                 `json:"run_id"`
	StepID          string                 `json:"step_id"`
	SessionID       string                 `json:"session_id,omitempty"`
	Status          string                 `json:"status"` // pending, running, completed, failed, skipped, awaiting_approval
	Outputs         map[string]interface{} `json:"outputs,omitempty"`
	StartedAt       *time.Time             `json:"started_at,omitempty"`
	FinishedAt      *time.Time             `json:"finished_at,omitempty"`
	TokensIn        int                    `json:"tokens_in,omitempty"`
	TokensOut       int                    `json:"tokens_out,omitempty"`
	DurationSeconds int                    `json:"duration_seconds,omitempty"`
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
		if err := we.updateStepStatus(ctx, run.ID, step.ID, "awaiting_approval", nil, nil); err != nil {
			return fmt.Errorf("marking step as awaiting approval: %w", err)
		}
		// TODO: Implement approval mechanism
		return nil
	}

	// Get the agent definition
	agentDef, err := we.defStore.GetAgentDefinition(ctx, step.Agent, run.Owner)
	if err != nil {
		return fmt.Errorf("getting agent definition %s: %w", step.Agent, err)
	}

	// Build TaskRequest from step and agent definition
	taskReq := agentDef.ToTaskRequest()
	if step.Repo != "" {
		taskReq.Repo = step.Repo
	}
	taskReq.TaskName = step.Agent
	taskReq.TriggerType = run.TriggerType
	taskReq.TriggerRef = run.TriggerRef

	// If step has a repo specified, override the agent's repo
	if step.Repo != "" {
		taskReq.Repo = step.Repo
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

	// Read token and duration information from the session
	tokensIn, tokensOut, durationSeconds, err := we.readStepTokenInfo(ctx, sessionID)
	if err != nil {
		log.Printf("error reading token info for step %s: %v", step.StepID, err)
		// Continue with 0 values
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

	// Update token information
	if tokensIn > 0 || tokensOut > 0 || durationSeconds > 0 {
		if err := we.updateStepTokens(ctx, run.ID, step.StepID, tokensIn, tokensOut, durationSeconds); err != nil {
			log.Printf("error updating tokens for step %s: %v", step.StepID, err)
			// Continue without failing the entire operation
		}
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
func (we *WorkflowEngine) evaluateCondition(ctx context.Context, runID string, condition string) (bool, error) {
	// Get accumulated step outputs
	stepOutputs, err := we.getRunStepOutputs(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("getting step outputs: %w", err)
	}

	// Simple condition evaluation
	// For now, support basic expressions like:
	// - "steps.implement.outcome == 'completed'"
	// - "steps.implement.outputs.status == 'success'"

	// Check for outcome conditions
	outcomePattern := regexp.MustCompile(`steps\.(\w+)\.outcome\s*==\s*'(\w+)'`)
	if matches := outcomePattern.FindStringSubmatch(condition); len(matches) == 3 {
		stepID := matches[1]
		expectedOutcome := matches[2]

		stepStatus, err := we.getStepStatus(ctx, runID, stepID)
		if err != nil {
			return false, fmt.Errorf("getting step status: %w", err)
		}

		return stepStatus == expectedOutcome, nil
	}

	// Check for output conditions
	outputPattern := regexp.MustCompile(`steps\.(\w+)\.outputs\.(\w+)\s*==\s*'([^']+)'`)
	if matches := outputPattern.FindStringSubmatch(condition); len(matches) == 4 {
		stepID := matches[1]
		outputKey := matches[2]
		expectedValue := matches[3]

		if stepOutput, exists := stepOutputs[stepID]; exists {
			if outputMap, ok := stepOutput.(map[string]interface{}); ok {
				if actualValue, exists := outputMap[outputKey]; exists {
					if str, ok := actualValue.(string); ok {
						return str == expectedValue, nil
					}
				}
			}
		}

		return false, nil
	}

	// Simple boolean values
	if condition == "true" {
		return true, nil
	}
	if condition == "false" {
		return false, nil
	}

	// Default to true for unknown conditions (to avoid blocking workflows)
	log.Printf("unknown condition format: %s", condition)
	return true, nil
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
		INSERT INTO workflow_run_steps (id, run_id, step_id, session_id, status, outputs, started_at, finished_at, tokens_in, tokens_out, duration_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, step.ID, step.RunID, step.StepID, step.SessionID, step.Status, outputsJSON, step.StartedAt, step.FinishedAt, step.TokensIn, step.TokensOut, step.DurationSeconds)

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
		SELECT id, run_id, step_id, session_id, status, outputs, started_at, finished_at,
		       COALESCE(tokens_in, 0), COALESCE(tokens_out, 0), COALESCE(duration_seconds, 0)
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

		if err := rows.Scan(&step.ID, &step.RunID, &step.StepID, &sessionID, &step.Status, &outputsJSON, &startedAt, &finishedAt, &step.TokensIn, &step.TokensOut, &step.DurationSeconds); err != nil {
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
		SELECT wrs.id, wrs.run_id, wrs.step_id, wrs.session_id, wrs.status, wrs.outputs, wrs.started_at, wrs.finished_at,
		       COALESCE(wrs.tokens_in, 0), COALESCE(wrs.tokens_out, 0), COALESCE(wrs.duration_seconds, 0),
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
		&step.ID, &step.RunID, &step.StepID, &sessionIDPtr, &step.Status, &stepOutputsJSON, &stepStartedAt, &stepFinishedAt,
		&step.TokensIn, &step.TokensOut, &step.DurationSeconds,
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

// updateStepTokens updates token and duration information for a workflow run step.
func (we *WorkflowEngine) updateStepTokens(ctx context.Context, runID, stepID string, tokensIn, tokensOut, durationSeconds int) error {
	_, err := we.db.Exec(ctx, `
		UPDATE workflow_run_steps
		SET tokens_in = $3, tokens_out = $4, duration_seconds = $5
		WHERE run_id = $1 AND step_id = $2
	`, runID, stepID, tokensIn, tokensOut, durationSeconds)

	return err
}

// ListWorkflowRuns returns workflow runs for a given owner, with optional status filtering.
func (we *WorkflowEngine) ListWorkflowRuns(ctx context.Context, owner string, status string, limit int, offset int) ([]WorkflowRun, int, error) {
	whereClause := "WHERE owner = $1"
	args := []interface{}{owner}
	argCount := 2

	if status != "" {
		whereClause += fmt.Sprintf(" AND status = $%d", argCount)
		args = append(args, status)
		argCount++
	}

	// Count total matching runs
	countQuery := "SELECT COUNT(*) FROM workflow_runs " + whereClause
	var total int
	if err := we.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting workflow runs: %w", err)
	}

	// Get the runs with pagination
	query := fmt.Sprintf(`
		SELECT id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, started_at, finished_at, owner, created_at
		FROM workflow_runs
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, whereClause, argCount, argCount+1)

	args = append(args, limit, offset)

	rows, err := we.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("querying workflow runs: %w", err)
	}
	defer rows.Close()

	var runs []WorkflowRun
	for rows.Next() {
		var run WorkflowRun
		var stepOutputsJSON []byte
		var triggerType, triggerRef, currentStep *string

		if err := rows.Scan(&run.ID, &run.WorkflowID, &run.Status, &triggerType, &triggerRef, &currentStep, &stepOutputsJSON, &run.StartedAt, &run.FinishedAt, &run.Owner, &run.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning workflow run: %w", err)
		}

		if triggerType != nil {
			run.TriggerType = *triggerType
		}
		if triggerRef != nil {
			run.TriggerRef = *triggerRef
		}
		if currentStep != nil {
			run.CurrentStep = *currentStep
		}

		if stepOutputsJSON != nil {
			if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
				log.Printf("error unmarshaling step outputs for run %s: %v", run.ID, err)
				run.StepOutputs = make(map[string]interface{})
			}
		} else {
			run.StepOutputs = make(map[string]interface{})
		}

		runs = append(runs, run)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterating workflow runs: %w", err)
	}

	if runs == nil {
		runs = []WorkflowRun{}
	}

	return runs, total, nil
}

// WorkflowRunDetail extends WorkflowRun with step details and workflow name.
type WorkflowRunDetail struct {
	WorkflowRun
	Workflow string            `json:"workflow"`
	Steps    []WorkflowRunStep `json:"steps"`
}

// GetWorkflowRunDetail returns detailed information about a workflow run including all steps.
func (we *WorkflowEngine) GetWorkflowRunDetail(ctx context.Context, runID, owner string) (*WorkflowRunDetail, error) {
	// Get the workflow run
	var run WorkflowRun
	var stepOutputsJSON []byte
	var triggerType, triggerRef, currentStep *string

	err := we.db.QueryRow(ctx, `
		SELECT id, workflow_id, status, trigger_type, trigger_ref, current_step, step_outputs, started_at, finished_at, owner, created_at
		FROM workflow_runs
		WHERE id = $1 AND owner = $2
	`, runID, owner).Scan(&run.ID, &run.WorkflowID, &run.Status, &triggerType, &triggerRef, &currentStep, &stepOutputsJSON, &run.StartedAt, &run.FinishedAt, &run.Owner, &run.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting workflow run %s: %w", runID, err)
	}

	if triggerType != nil {
		run.TriggerType = *triggerType
	}
	if triggerRef != nil {
		run.TriggerRef = *triggerRef
	}
	if currentStep != nil {
		run.CurrentStep = *currentStep
	}

	if stepOutputsJSON != nil {
		if err := json.Unmarshal(stepOutputsJSON, &run.StepOutputs); err != nil {
			log.Printf("error unmarshaling step outputs for run %s: %v", run.ID, err)
			run.StepOutputs = make(map[string]interface{})
		}
	} else {
		run.StepOutputs = make(map[string]interface{})
	}

	// Get the workflow definition to get workflow name
	workflow, err := we.getWorkflowByID(ctx, run.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("getting workflow definition: %w", err)
	}

	// Get all steps for this run
	steps, err := we.getWorkflowRunSteps(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("getting workflow run steps: %w", err)
	}

	return &WorkflowRunDetail{
		WorkflowRun: run,
		Workflow:    workflow.Name,
		Steps:       steps,
	}, nil
}

// CancelWorkflowRun cancels a running workflow run.
func (we *WorkflowEngine) CancelWorkflowRun(ctx context.Context, runID, owner string) error {
	// Check that the user owns this run and it's in a cancelable state
	var currentStatus string
	err := we.db.QueryRow(ctx, `
		SELECT status FROM workflow_runs WHERE id = $1 AND owner = $2
	`, runID, owner).Scan(&currentStatus)
	if err != nil {
		return fmt.Errorf("workflow run not found or access denied")
	}

	if currentStatus != "running" && currentStatus != "pending" {
		return fmt.Errorf("workflow run is not in a cancelable state (current status: %s)", currentStatus)
	}

	// Update the run status to cancelled
	now := time.Now().UTC()
	if err := we.updateWorkflowRunStatus(ctx, runID, "cancelled", nil, &now); err != nil {
		return fmt.Errorf("updating workflow run status: %w", err)
	}

	// Cancel any running sessions for this workflow run
	steps, err := we.getWorkflowRunSteps(ctx, runID)
	if err != nil {
		log.Printf("error getting steps for cancelled run %s: %v", runID, err)
		return nil // Don't fail the cancellation if we can't get steps
	}

	for _, step := range steps {
		if step.Status == "running" && step.SessionID != "" {
			// Try to cancel the session, but don't fail if we can't
			// (the session might have already finished)
			log.Printf("cancelling session %s for cancelled workflow run %s", step.SessionID, runID)
			// Note: We would need access to the dispatcher to cancel sessions
			// For now, we'll just mark the step as cancelled
			if err := we.updateStepStatus(ctx, runID, step.StepID, "cancelled", &now, nil); err != nil {
				log.Printf("error marking step %s as cancelled: %v", step.StepID, err)
			}
		}
	}

	return nil
}

// readStepTokenInfo reads token and duration information from a session.
// Returns (tokensIn, tokensOut, durationSeconds, error).
func (we *WorkflowEngine) readStepTokenInfo(ctx context.Context, sessionID string) (int, int, int, error) {
	// For now, we'll try to extract duration from session timestamps and set tokens to 0
	// In the future, this could be enhanced to read from Claude's transcript events
	// or from a separate token tracking mechanism

	var startedAt, finishedAt *time.Time
	err := we.db.QueryRow(ctx, `
		SELECT started_at, finished_at FROM sessions WHERE id = $1
	`, sessionID).Scan(&startedAt, &finishedAt)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("getting session timestamps: %w", err)
	}

	durationSeconds := 0
	if startedAt != nil && finishedAt != nil {
		durationSeconds = int(finishedAt.Sub(*startedAt).Seconds())
	}

	// TODO: In the future, extract token counts from transcript events
	// For now, return 0 for tokens
	return 0, 0, durationSeconds, nil
}
