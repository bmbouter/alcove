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
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// WorkflowDefinition represents a complete workflow defined in YAML.
type WorkflowDefinition struct {
	Name     string         `json:"name" yaml:"name"`
	Workflow []WorkflowStep `json:"workflow" yaml:"workflow"`

	// Metadata (not from YAML).
	SourceRepo string `json:"source_repo,omitempty"`
	SourceFile string `json:"source_file,omitempty"`
	Owner      string `json:"owner,omitempty"`
}

// WorkflowStep represents a single step in a workflow.
type WorkflowStep struct {
	ID        string                 `json:"id" yaml:"id"`
	Agent     string                 `json:"agent" yaml:"agent"`
	Repo      string                 `json:"repo,omitempty" yaml:"repo,omitempty"`
	Trigger   *EventTrigger          `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Needs     []string               `json:"needs,omitempty" yaml:"needs,omitempty"`
	Condition string                 `json:"condition,omitempty" yaml:"condition,omitempty"`
	Approval  string                 `json:"approval,omitempty" yaml:"approval,omitempty"` // "required" or empty
	Outputs   []string               `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Inputs    map[string]interface{} `json:"inputs,omitempty" yaml:"inputs,omitempty"`
}

// WorkflowTrigger defines when a workflow should be triggered.
type WorkflowTrigger struct {
	GitHub *GitHubTrigger `json:"github,omitempty" yaml:"github,omitempty"`
}

// ParseWorkflowDefinition parses a YAML byte slice into a WorkflowDefinition and
// validates required fields and dependencies.
func ParseWorkflowDefinition(data []byte) (*WorkflowDefinition, error) {
	var wd WorkflowDefinition
	if err := yaml.Unmarshal(data, &wd); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if wd.Name == "" {
		return nil, fmt.Errorf("workflow definition missing required field: name")
	}

	if len(wd.Workflow) == 0 {
		return nil, fmt.Errorf("workflow definition must contain at least one step")
	}

	if err := validateWorkflowSteps(wd.Workflow); err != nil {
		return nil, fmt.Errorf("workflow validation error: %w", err)
	}

	return &wd, nil
}

// validateWorkflowSteps performs comprehensive validation on workflow steps.
func validateWorkflowSteps(steps []WorkflowStep) error {
	stepIDs := make(map[string]bool)
	stepMap := make(map[string]WorkflowStep)

	// First pass: check for required fields and collect step IDs
	for _, step := range steps {
		if step.ID == "" {
			return fmt.Errorf("workflow step missing required field: id")
		}
		if step.Agent == "" {
			return fmt.Errorf("workflow step '%s' missing required field: agent", step.ID)
		}

		// Check for duplicate step IDs
		if stepIDs[step.ID] {
			return fmt.Errorf("duplicate step ID: %s", step.ID)
		}
		stepIDs[step.ID] = true
		stepMap[step.ID] = step

		// Validate approval field
		if step.Approval != "" && step.Approval != "required" {
			return fmt.Errorf("workflow step '%s' has invalid approval value '%s' (must be 'required' or empty)", step.ID, step.Approval)
		}

		// Validate condition syntax (basic check for template variables)
		if step.Condition != "" {
			if err := validateConditionSyntax(step.Condition); err != nil {
				return fmt.Errorf("workflow step '%s' has invalid condition: %w", step.ID, err)
			}
		}

		// Validate inputs template syntax
		if err := validateInputsTemplateSyntax(step.Inputs); err != nil {
			return fmt.Errorf("workflow step '%s' has invalid inputs: %w", step.ID, err)
		}
	}

	// Second pass: validate dependencies and check for circular references
	for _, step := range steps {
		for _, dep := range step.Needs {
			if !stepIDs[dep] {
				return fmt.Errorf("workflow step '%s' references non-existent dependency: %s", step.ID, dep)
			}
		}
	}

	// Check for circular dependencies using DFS
	if err := checkCircularDependencies(steps); err != nil {
		return err
	}

	return nil
}

// validateConditionSyntax performs basic validation of condition expressions.
func validateConditionSyntax(condition string) error {
	// Basic check: ensure it contains template-like syntax for step references
	if strings.Contains(condition, "steps.") && strings.Contains(condition, ".outcome") {
		return nil // Valid format like "steps.implement.outcome == 'completed'"
	}
	if strings.Contains(condition, "steps.") && strings.Contains(condition, ".outputs.") {
		return nil // Valid format like "steps.implement.outputs.status == 'success'"
	}

	// Allow simple boolean expressions
	if condition == "true" || condition == "false" {
		return nil
	}

	// For now, accept any non-empty condition as potentially valid
	// More sophisticated validation could be added later
	return nil
}

// validateInputsTemplateSyntax validates template syntax in inputs.
func validateInputsTemplateSyntax(inputs map[string]interface{}) error {
	for key, value := range inputs {
		if str, ok := value.(string); ok {
			// Check for template syntax like "{{steps.implement.outputs.summary}}"
			if strings.Contains(str, "{{") && strings.Contains(str, "}}") {
				if !strings.Contains(str, "steps.") {
					return fmt.Errorf("input '%s' contains invalid template syntax: %s", key, str)
				}
			}
		}
	}
	return nil
}

// checkCircularDependencies uses depth-first search to detect circular dependencies.
func checkCircularDependencies(steps []WorkflowStep) error {
	// Build adjacency map
	graph := make(map[string][]string)
	for _, step := range steps {
		graph[step.ID] = step.Needs
	}

	// Track visited nodes during DFS
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(node string) bool {
		visited[node] = true
		recStack[node] = true

		for _, neighbor := range graph[node] {
			if !visited[neighbor] {
				if hasCycle(neighbor) {
					return true
				}
			} else if recStack[neighbor] {
				return true
			}
		}

		recStack[node] = false
		return false
	}

	// Check each node for cycles
	for _, step := range steps {
		if !visited[step.ID] {
			if hasCycle(step.ID) {
				return fmt.Errorf("circular dependency detected in workflow")
			}
		}
	}

	return nil
}

// GetRootSteps returns workflow steps that have no dependencies (no "needs").
func (wd *WorkflowDefinition) GetRootSteps() []WorkflowStep {
	var roots []WorkflowStep
	for _, step := range wd.Workflow {
		if len(step.Needs) == 0 {
			roots = append(roots, step)
		}
	}
	return roots
}

// GetStepByID returns a workflow step by its ID.
func (wd *WorkflowDefinition) GetStepByID(id string) *WorkflowStep {
	for i := range wd.Workflow {
		if wd.Workflow[i].ID == id {
			return &wd.Workflow[i]
		}
	}
	return nil
}

// GetDependents returns all steps that depend on the given step ID.
func (wd *WorkflowDefinition) GetDependents(stepID string) []WorkflowStep {
	var dependents []WorkflowStep
	for _, step := range wd.Workflow {
		for _, dep := range step.Needs {
			if dep == stepID {
				dependents = append(dependents, step)
				break
			}
		}
	}
	return dependents
}

// Extended WorkflowDefinition with storage metadata
type StoredWorkflowDefinition struct {
	WorkflowDefinition
	ID         string    `json:"id"`
	SourceKey  string    `json:"source_key"`
	RawYAML    string    `json:"raw_yaml"`
	SyncError  string    `json:"sync_error,omitempty"`
	LastSynced time.Time `json:"last_synced"`
}

// WorkflowStore manages workflow definitions in PostgreSQL.
type WorkflowStore struct {
	db *pgxpool.Pool
}

// NewWorkflowStore creates a WorkflowStore with the given database pool.
func NewWorkflowStore(db *pgxpool.Pool) *WorkflowStore {
	return &WorkflowStore{db: db}
}

// UpsertWorkflow inserts or updates a workflow definition by source_key.
func (s *WorkflowStore) UpsertWorkflow(ctx context.Context, wd *WorkflowDefinition, sourceKey, rawYAML, syncError string) error {
	id := uuid.New().String()

	parsedJSON, err := json.Marshal(wd)
	if err != nil {
		return fmt.Errorf("marshaling workflow definition: %w", err)
	}

	now := time.Now().UTC()

	_, err = s.db.Exec(ctx, `
		INSERT INTO workflows (id, name, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, owner, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (source_key) DO UPDATE SET
			name = EXCLUDED.name,
			source_repo = EXCLUDED.source_repo,
			source_file = EXCLUDED.source_file,
			raw_yaml = EXCLUDED.raw_yaml,
			parsed = EXCLUDED.parsed,
			sync_error = EXCLUDED.sync_error,
			last_synced = EXCLUDED.last_synced,
			owner = EXCLUDED.owner,
			updated_at = EXCLUDED.updated_at
	`, id, wd.Name, wd.SourceRepo, wd.SourceFile, sourceKey, rawYAML, parsedJSON, nilIfEmpty(syncError), now, wd.Owner, now, now)
	if err != nil {
		return fmt.Errorf("upserting workflow: %w", err)
	}

	return nil
}

// ListWorkflowsByRepo returns all workflow definitions from a given repo URL and owner.
func (s *WorkflowStore) ListWorkflowsByRepo(ctx context.Context, repoURL, owner string) ([]StoredWorkflowDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, owner
		FROM workflows
		WHERE source_repo = $1 AND owner = $2
		ORDER BY name ASC
	`, repoURL, owner)
	if err != nil {
		return nil, fmt.Errorf("querying workflows for repo %s: %w", repoURL, err)
	}
	defer rows.Close()

	var workflows []StoredWorkflowDefinition
	for rows.Next() {
		var swd StoredWorkflowDefinition
		var parsedJSON []byte
		var syncError *string

		if err := rows.Scan(
			&swd.ID, &swd.Name, &swd.SourceRepo, &swd.SourceFile,
			&swd.SourceKey, &swd.RawYAML, &parsedJSON, &syncError, &swd.LastSynced, &swd.Owner,
		); err != nil {
			return nil, fmt.Errorf("scanning workflow: %w", err)
		}

		if syncError != nil {
			swd.SyncError = *syncError
		}

		// Unmarshal the parsed JSON back into the workflow definition
		if parsedJSON != nil {
			var wd WorkflowDefinition
			if err := json.Unmarshal(parsedJSON, &wd); err == nil {
				swd.WorkflowDefinition = wd
			}
		}

		workflows = append(workflows, swd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workflows: %w", err)
	}

	if workflows == nil {
		workflows = []StoredWorkflowDefinition{}
	}
	return workflows, nil
}

// DeleteWorkflowsByRepo removes all workflow definitions from a given repo URL and owner.
func (s *WorkflowStore) DeleteWorkflowsByRepo(ctx context.Context, repoURL, owner string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM workflows WHERE source_repo = $1 AND owner = $2`, repoURL, owner)
	if err != nil {
		return fmt.Errorf("deleting workflows for repo %s: %w", repoURL, err)
	}
	return nil
}

// ListWorkflows returns all workflow definitions owned by the given user.
func (s *WorkflowStore) ListWorkflows(ctx context.Context, owner string) ([]StoredWorkflowDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, owner
		FROM workflows
		WHERE owner = $1
		ORDER BY name ASC
	`, owner)
	if err != nil {
		return nil, fmt.Errorf("querying workflows for owner %s: %w", owner, err)
	}
	defer rows.Close()

	var workflows []StoredWorkflowDefinition
	for rows.Next() {
		var swd StoredWorkflowDefinition
		var parsedJSON []byte
		var syncError *string

		if err := rows.Scan(
			&swd.ID, &swd.Name, &swd.SourceRepo, &swd.SourceFile,
			&swd.SourceKey, &swd.RawYAML, &parsedJSON, &syncError, &swd.LastSynced, &swd.Owner,
		); err != nil {
			return nil, fmt.Errorf("scanning workflow: %w", err)
		}

		if syncError != nil {
			swd.SyncError = *syncError
		}

		// Unmarshal the parsed JSON back into the workflow definition
		if parsedJSON != nil {
			var wd WorkflowDefinition
			if err := json.Unmarshal(parsedJSON, &wd); err == nil {
				swd.WorkflowDefinition = wd
			}
		}

		workflows = append(workflows, swd)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating workflows: %w", err)
	}

	if workflows == nil {
		workflows = []StoredWorkflowDefinition{}
	}
	return workflows, nil
}

// ValidateWorkflowAgentReferences checks that all agents referenced in the workflow
// exist in the given agent definitions. Returns a list of missing agent names.
func (s *WorkflowStore) ValidateWorkflowAgentReferences(ctx context.Context, wd *WorkflowDefinition, agentDefs []TaskDefinition) []string {
	agentNames := make(map[string]bool)
	for _, def := range agentDefs {
		agentNames[def.Name] = true
	}

	var missing []string
	for _, step := range wd.Workflow {
		if !agentNames[step.Agent] {
			missing = append(missing, step.Agent)
		}
	}

	return missing
}
