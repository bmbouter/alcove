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
	"gopkg.in/yaml.v3"
)

// WorkflowDefinition represents a complete workflow defined in YAML.
type WorkflowDefinition struct {
	Name     string         `json:"name" yaml:"name"`
	Workflow []WorkflowStep `json:"workflow" yaml:"workflow"`
	Trigger  *EventTrigger  `json:"trigger,omitempty" yaml:"trigger,omitempty"`

	// Metadata (not from YAML).
	SourceRepo string `json:"source_repo,omitempty"`
	SourceFile string `json:"source_file,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
}

// WorkflowStep represents a single step in a workflow.
type WorkflowStep struct {
	ID            string                 `json:"id" yaml:"id"`
	Agent         string                 `json:"agent,omitempty" yaml:"agent,omitempty"`
	Type          string                 `json:"type,omitempty" yaml:"type,omitempty"`                     // "agent" (default) or "bridge"
	Action        string                 `json:"action,omitempty" yaml:"action,omitempty"`                 // Bridge action name (create-pr, await-ci, merge-pr)
	Repo          string                 `json:"repo,omitempty" yaml:"repo,omitempty"`
	Trigger       *EventTrigger          `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Needs         []string               `json:"needs,omitempty" yaml:"needs,omitempty"`
	Depends       string                 `json:"depends,omitempty" yaml:"depends,omitempty"`               // Enhanced dependency expression
	Condition     string                 `json:"condition,omitempty" yaml:"condition,omitempty"`
	Approval      string                 `json:"approval,omitempty" yaml:"approval,omitempty"`             // "required" or empty
	Outputs       []string               `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Inputs        map[string]interface{} `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	RouteField    string                 `json:"route_field,omitempty" yaml:"route_field,omitempty"`       // Field name for routing decisions
	RouteMap      map[string]string      `json:"route_map,omitempty" yaml:"route_map,omitempty"`           // Value -> next step mapping
	MaxIterations int                    `json:"max_iterations,omitempty" yaml:"max_iterations,omitempty"` // Max times this step can execute (default 1)
	MaxRetries     int                    `json:"max_retries,omitempty" yaml:"max_retries,omitempty"`       // Max retries on failure within one iteration
	Credentials    map[string]string      `json:"credentials,omitempty" yaml:"credentials,omitempty"`
	DirectOutbound bool                   `json:"direct_outbound,omitempty" yaml:"direct_outbound,omitempty"`
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

	// Validate workflow-level trigger if present.
	if wd.Trigger != nil {
		if wd.Trigger.GitHub != nil && len(wd.Trigger.GitHub.Events) == 0 {
			return nil, fmt.Errorf("workflow trigger.github block present but events list is empty")
		}
	}

	return &wd, nil
}

// validBridgeActions lists the allowed bridge action names.
var validBridgeActions = map[string]bool{
	"create-pr": true,
	"await-ci":  true,
	"merge-pr":  true,
}

// validateWorkflowSteps performs comprehensive validation on workflow steps.
func validateWorkflowSteps(steps []WorkflowStep) error {
	stepIDs := make(map[string]bool)
	stepMap := make(map[string]WorkflowStep)

	// First pass: check for required fields and collect step IDs
	for i := range steps {
		step := &steps[i]
		if step.ID == "" {
			return fmt.Errorf("workflow step missing required field: id")
		}

		// Determine effective type.
		stepType := step.Type
		if stepType == "" {
			stepType = "agent"
		}

		switch stepType {
		case "agent":
			if step.Agent == "" {
				return fmt.Errorf("workflow step '%s' missing required field: agent", step.ID)
			}
		case "bridge":
			if step.Action == "" {
				return fmt.Errorf("workflow step '%s' of type 'bridge' missing required field: action", step.ID)
			}
			if !validBridgeActions[step.Action] {
				return fmt.Errorf("workflow step '%s' has invalid bridge action '%s' (must be one of: create-pr, await-ci, merge-pr)", step.ID, step.Action)
			}
		default:
			return fmt.Errorf("workflow step '%s' has invalid type '%s' (must be 'agent' or 'bridge')", step.ID, step.Type)
		}

		// Check for duplicate step IDs
		if stepIDs[step.ID] {
			return fmt.Errorf("duplicate step ID: %s", step.ID)
		}
		stepIDs[step.ID] = true
		stepMap[step.ID] = *step

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

		// Validate field-based routing configuration
		if step.RouteField != "" || len(step.RouteMap) > 0 {
			if err := validateRouteConfiguration(step.RouteField, step.RouteMap); err != nil {
				return fmt.Errorf("workflow step '%s' has invalid routing configuration: %w", step.ID, err)
			}
		}

		// Validate max_iterations
		if step.MaxIterations < 0 {
			return fmt.Errorf("workflow step '%s' has invalid max_iterations: must be positive", step.ID)
		}

		// Backward compat: if Needs is populated and Depends is empty, auto-generate Depends.
		if len(step.Needs) > 0 && step.Depends == "" {
			step.Depends = NeedsToDepends(step.Needs)
		}

		// Validate depends expression syntax (try to parse it).
		if step.Depends != "" {
			_, err := EvaluateDepends(step.Depends, map[string]string{})
			if err != nil {
				// Distinguish parse errors from "step not found" (which is expected with empty map).
				// Re-tokenize and check for syntax issues.
				if _, tokenErr := tokenizeDepends(step.Depends); tokenErr != nil {
					return fmt.Errorf("workflow step '%s' has invalid depends expression: %w", step.ID, tokenErr)
				}
			}
		}
	}

	// Second pass: validate dependencies (both Needs and Depends references)
	for _, step := range steps {
		for _, dep := range step.Needs {
			if !stepIDs[dep] {
				return fmt.Errorf("workflow step '%s' references non-existent dependency: %s", step.ID, dep)
			}
		}

		// Validate step IDs referenced in Depends expressions.
		if step.Depends != "" {
			referencedIDs := ExtractDependsStepIDs(step.Depends)
			for _, refID := range referencedIDs {
				if !stepIDs[refID] {
					return fmt.Errorf("workflow step '%s' depends expression references non-existent step: %s", step.ID, refID)
				}
			}
		}
	}

	// Build dependency graph for cycle detection using both Needs and Depends.
	graph := make(map[string][]string)
	for _, step := range steps {
		var deps []string
		if step.Depends != "" {
			deps = ExtractDependsStepIDs(step.Depends)
		} else {
			deps = step.Needs
		}
		graph[step.ID] = deps
	}

	// Check for circular dependencies using DFS.
	if hasCycles := detectCycles(graph); hasCycles {
		// Cycles are only allowed if ALL cycle participants have max_iterations > 1 (bounded cycles).
		cycleParticipants := findCycleParticipants(graph)
		allBounded := true
		for _, stepID := range cycleParticipants {
			s := stepMap[stepID]
			if s.MaxIterations <= 1 {
				allBounded = false
				log.Printf("WARNING: workflow step '%s' participates in a cycle but has max_iterations=%d (should be > 1 for bounded cycles)", stepID, s.MaxIterations)
			}
		}
		if !allBounded {
			return fmt.Errorf("circular dependency detected in workflow")
		}
	}

	return nil
}

// detectCycles checks if the dependency graph contains any cycles.
func detectCycles(graph map[string][]string) bool {
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

	for node := range graph {
		if !visited[node] {
			if hasCycle(node) {
				return true
			}
		}
	}

	return false
}

// findCycleParticipants returns all step IDs that are part of a cycle.
func findCycleParticipants(graph map[string][]string) []string {
	var participants []string

	for node := range graph {
		// Check if this node can reach itself.
		visited := make(map[string]bool)
		if canReach(graph, node, node, visited, true) {
			participants = append(participants, node)
		}
	}

	return participants
}

// canReach checks if 'target' is reachable from 'current' in the graph.
// 'firstStep' indicates whether this is the first call (skip self-check on first step).
func canReach(graph map[string][]string, current, target string, visited map[string]bool, firstStep bool) bool {
	if !firstStep && current == target {
		return true
	}
	if visited[current] {
		return false
	}
	visited[current] = true

	for _, neighbor := range graph[current] {
		if canReach(graph, neighbor, target, visited, false) {
			return true
		}
	}

	return false
}

// validateConditionSyntax performs validation of condition expressions using the condition evaluator.
func validateConditionSyntax(condition string) error {
	// Import the condition package functionality locally to avoid import cycle
	// For now, use a simplified validation that accepts the new syntax
	condition = strings.TrimSpace(condition)

	if condition == "" || condition == "true" || condition == "false" {
		return nil
	}

	// Basic validation for supported condition patterns
	patterns := []string{
		`steps\.(\w+)\.outcome\s*(==|!=)\s*'([^']*)'`,
		`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*'([^']*)'`,
		`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*(\d+(?:\.\d+)?)`,
	}

	// Handle complex expressions with boolean operators
	if strings.Contains(condition, "&&") || strings.Contains(condition, "||") {
		return validateComplexConditionSyntax(condition, patterns)
	}

	return validateSimpleConditionSyntax(condition, patterns)
}

// validateComplexConditionSyntax validates conditions with boolean operators.
func validateComplexConditionSyntax(condition string, patterns []string) error {
	// Split by operators and validate each part
	orParts := splitByOperatorForValidation(condition, "||")

	for _, orPart := range orParts {
		andParts := splitByOperatorForValidation(orPart, "&&")
		for _, andPart := range andParts {
			if err := validateSimpleConditionSyntax(strings.TrimSpace(andPart), patterns); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateSimpleConditionSyntax validates a single condition expression.
func validateSimpleConditionSyntax(condition string, patterns []string) error {
	for _, pattern := range patterns {
		matched, err := regexp.MatchString(pattern, condition)
		if err != nil {
			return fmt.Errorf("error matching pattern: %w", err)
		}
		if matched {
			return nil
		}
	}

	return fmt.Errorf("invalid condition syntax: %s", condition)
}

// splitByOperatorForValidation splits a string by an operator, respecting quotes.
func splitByOperatorForValidation(s, operator string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			inQuotes = !inQuotes
			current.WriteByte(s[i])
			continue
		}

		if !inQuotes && i+len(operator) <= len(s) && s[i:i+len(operator)] == operator {
			parts = append(parts, current.String())
			current.Reset()
			i += len(operator) - 1 // -1 because loop will increment
			continue
		}

		current.WriteByte(s[i])
	}

	parts = append(parts, current.String())
	return parts
}

// validateInputsTemplateSyntax validates template syntax in inputs.
func validateInputsTemplateSyntax(inputs map[string]interface{}) error {
	for key, value := range inputs {
		if str, ok := value.(string); ok {
			// Check for template syntax like "{{steps.implement.outputs.summary}}" or "{{trigger.issue_number}}"
			if strings.Contains(str, "{{") && strings.Contains(str, "}}") {
				if !strings.Contains(str, "steps.") &&
				   !strings.Contains(str, "trigger.issue_number") &&
				   !strings.Contains(str, "trigger.issue_title") &&
				   !strings.Contains(str, "trigger.issue_body") &&
				   !strings.Contains(str, "trigger.issue_url") {
					return fmt.Errorf("input '%s' contains invalid template syntax: %s", key, str)
				}
			}
		}
	}
	return nil
}

// validateRouteConfiguration validates field-based routing configuration.
func validateRouteConfiguration(routeField string, routeMap map[string]string) error {
	if routeField == "" && len(routeMap) > 0 {
		return fmt.Errorf("route_map specified but route_field is empty")
	}
	if routeField != "" && len(routeMap) == 0 {
		return fmt.Errorf("route_field specified but route_map is empty")
	}
	if routeField == "" && len(routeMap) == 0 {
		return nil // Nothing to validate
	}

	// Validate route field name (simple identifier validation)
	if !isValidIdentifier(routeField) {
		return fmt.Errorf("invalid route_field name: %s", routeField)
	}

	// Validate route map values (step IDs will be validated in dependency check)
	for key, value := range routeMap {
		if key == "" {
			return fmt.Errorf("empty key in route_map")
		}
		if value == "" {
			return fmt.Errorf("empty value in route_map for key '%s'", key)
		}
	}

	return nil
}

// isValidIdentifier checks if a string is a valid identifier (alphanumeric + underscore).
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
				return false
			}
		}
	}
	return true
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

// GetRootSteps returns workflow steps that have no dependencies (no "needs" and no "depends").
func (wd *WorkflowDefinition) GetRootSteps() []WorkflowStep {
	var roots []WorkflowStep
	for _, step := range wd.Workflow {
		if len(step.Needs) == 0 && step.Depends == "" {
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
// This checks both the legacy Needs field and the Depends expression.
func (wd *WorkflowDefinition) GetDependents(stepID string) []WorkflowStep {
	var dependents []WorkflowStep
	for _, step := range wd.Workflow {
		// Check Depends expression first (it takes precedence).
		if step.Depends != "" {
			referencedIDs := ExtractDependsStepIDs(step.Depends)
			for _, refID := range referencedIDs {
				if refID == stepID {
					dependents = append(dependents, step)
					break
				}
			}
			continue
		}
		// Fall back to legacy Needs field.
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
		INSERT INTO workflows (id, name, source_repo, source_file, source_key, raw_yaml, parsed, definition, sync_error, last_synced, team_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (source_key, team_id) DO UPDATE SET
			name = EXCLUDED.name,
			source_repo = EXCLUDED.source_repo,
			source_file = EXCLUDED.source_file,
			raw_yaml = EXCLUDED.raw_yaml,
			parsed = EXCLUDED.parsed,
			definition = EXCLUDED.definition,
			sync_error = EXCLUDED.sync_error,
			last_synced = EXCLUDED.last_synced,
			team_id = EXCLUDED.team_id,
			updated_at = EXCLUDED.updated_at
	`, id, wd.Name, wd.SourceRepo, wd.SourceFile, sourceKey, rawYAML, parsedJSON, parsedJSON, nilIfEmpty(syncError), now, wd.TeamID, now, now)
	if err != nil {
		return fmt.Errorf("upserting workflow: %w", err)
	}

	return nil
}

// ListWorkflowsByRepo returns all workflow definitions from a given repo URL and owner.
func (s *WorkflowStore) ListWorkflowsByRepo(ctx context.Context, repoURL, teamID string) ([]StoredWorkflowDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, team_id
		FROM workflows
		WHERE source_repo = $1 AND team_id = $2
		ORDER BY name ASC
	`, repoURL, teamID)
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
			&swd.SourceKey, &swd.RawYAML, &parsedJSON, &syncError, &swd.LastSynced, &swd.TeamID,
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
func (s *WorkflowStore) DeleteWorkflowsByRepo(ctx context.Context, repoURL, teamID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM workflows WHERE source_repo = $1 AND team_id = $2`, repoURL, teamID)
	if err != nil {
		return fmt.Errorf("deleting workflows for repo %s: %w", repoURL, err)
	}
	return nil
}

// ListWorkflows returns all workflow definitions owned by the given user.
func (s *WorkflowStore) ListWorkflows(ctx context.Context, teamID string) ([]StoredWorkflowDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, team_id
		FROM workflows
		WHERE team_id = $1
		ORDER BY name ASC
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying workflows for team %s: %w", teamID, err)
	}
	defer rows.Close()

	var workflows []StoredWorkflowDefinition
	for rows.Next() {
		var swd StoredWorkflowDefinition
		var parsedJSON []byte
		var syncError *string

		if err := rows.Scan(
			&swd.ID, &swd.Name, &swd.SourceRepo, &swd.SourceFile,
			&swd.SourceKey, &swd.RawYAML, &parsedJSON, &syncError, &swd.LastSynced, &swd.TeamID,
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
// Bridge-type steps are skipped since they don't reference agents.
func (s *WorkflowStore) ValidateWorkflowAgentReferences(ctx context.Context, wd *WorkflowDefinition, agentDefs []TaskDefinition) []string {
	agentNames := make(map[string]bool)
	for _, def := range agentDefs {
		agentNames[def.Name] = true
	}

	var missing []string
	for _, step := range wd.Workflow {
		// Bridge steps don't need agents.
		if step.Type == "bridge" {
			continue
		}
		if step.Agent != "" && !agentNames[step.Agent] {
			missing = append(missing, step.Agent)
		}
	}

	return missing
}
