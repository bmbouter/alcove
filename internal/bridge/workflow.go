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
	"fmt"
	"strings"

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
