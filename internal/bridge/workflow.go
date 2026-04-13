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
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowTrigger defines when a workflow should be triggered by external events.
type WorkflowTrigger struct {
	GitHub *GitHubTrigger `json:"github,omitempty" yaml:"github"`
}

// WorkflowStep represents a single step in a workflow.
type WorkflowStep struct {
	ID        string                 `json:"id" yaml:"id"`
	Agent     string                 `json:"agent" yaml:"agent"`
	Repo      string                 `json:"repo,omitempty" yaml:"repo"`
	Trigger   *WorkflowTrigger       `json:"trigger,omitempty" yaml:"trigger"`
	Needs     []string               `json:"needs,omitempty" yaml:"needs"`
	Condition string                 `json:"condition,omitempty" yaml:"condition"`
	Approval  string                 `json:"approval,omitempty" yaml:"approval"`
	Outputs   []string               `json:"outputs,omitempty" yaml:"outputs"`
	Inputs    map[string]interface{} `json:"inputs,omitempty" yaml:"inputs"`
}

// WorkflowDefinition represents a complete workflow configuration.
type WorkflowDefinition struct {
	Name     string         `json:"name" yaml:"name"`
	Workflow []WorkflowStep `json:"workflow" yaml:"workflow"`
}

// ParseWorkflowDefinition parses a YAML byte slice into a WorkflowDefinition and
// validates required fields and workflow integrity.
func ParseWorkflowDefinition(data []byte) (*WorkflowDefinition, error) {
	var wd WorkflowDefinition
	if err := yaml.Unmarshal(data, &wd); err != nil {
		return nil, fmt.Errorf("parsing workflow YAML: %w", err)
	}

	if wd.Name == "" {
		return nil, fmt.Errorf("workflow definition missing required field: name")
	}

	if len(wd.Workflow) == 0 {
		return nil, fmt.Errorf("workflow definition must contain at least one step")
	}

	// Validate workflow steps
	if err := validateWorkflowSteps(wd.Workflow); err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	return &wd, nil
}

// validateWorkflowSteps performs validation on workflow steps including:
// - Unique step IDs
// - No circular dependencies
// - Referenced steps exist in needs dependencies
func validateWorkflowSteps(steps []WorkflowStep) error {
	stepIDs := make(map[string]bool)
	stepMap := make(map[string]*WorkflowStep)

	// First pass: collect all step IDs and check uniqueness
	for i := range steps {
		step := &steps[i]

		if step.ID == "" {
			return fmt.Errorf("step at position %d missing required field: id", i)
		}

		if step.Agent == "" {
			return fmt.Errorf("step '%s' missing required field: agent", step.ID)
		}

		if stepIDs[step.ID] {
			return fmt.Errorf("duplicate step ID: %s", step.ID)
		}

		stepIDs[step.ID] = true
		stepMap[step.ID] = step

		// Validate approval field if present
		if step.Approval != "" && step.Approval != "required" {
			return fmt.Errorf("step '%s' has invalid approval value '%s' (must be 'required' or empty)", step.ID, step.Approval)
		}
	}

	// Second pass: validate dependencies and check for circular references
	for _, step := range steps {
		// Validate that all dependencies exist
		for _, need := range step.Needs {
			if !stepIDs[need] {
				return fmt.Errorf("step '%s' references non-existent dependency: %s", step.ID, need)
			}
		}

		// Check for circular dependencies
		if err := detectCircularDependency(step.ID, stepMap, make(map[string]bool), make(map[string]bool)); err != nil {
			return err
		}
	}

	return nil
}

// detectCircularDependency performs a depth-first search to detect circular dependencies
func detectCircularDependency(stepID string, stepMap map[string]*WorkflowStep, visiting, visited map[string]bool) error {
	if visiting[stepID] {
		return fmt.Errorf("circular dependency detected involving step: %s", stepID)
	}

	if visited[stepID] {
		return nil
	}

	step := stepMap[stepID]
	if step == nil {
		return nil
	}

	visiting[stepID] = true

	for _, need := range step.Needs {
		if err := detectCircularDependency(need, stepMap, visiting, visited); err != nil {
			return err
		}
	}

	visiting[stepID] = false
	visited[stepID] = true

	return nil
}

// ValidateAgentReference checks if the specified agent exists in the given context.
// This is a placeholder for future integration with agent registry validation.
func ValidateAgentReference(ctx context.Context, agentName string) error {
	// TODO: Implement agent registry lookup when agent storage is available
	if agentName == "" {
		return fmt.Errorf("agent name cannot be empty")
	}

	// For now, we'll just validate that the agent name is not empty
	// In the future, this should check against an actual agent registry
	return nil
}

// GetWorkflowDAG returns the workflow steps organized as a dependency graph.
// Returns a map where keys are step IDs and values are slices of step IDs they depend on.
func (wd *WorkflowDefinition) GetWorkflowDAG() map[string][]string {
	dag := make(map[string][]string)

	for _, step := range wd.Workflow {
		dag[step.ID] = step.Needs
	}

	return dag
}

// GetRootSteps returns workflow steps that have no dependencies (entry points).
func (wd *WorkflowDefinition) GetRootSteps() []WorkflowStep {
	var roots []WorkflowStep

	for _, step := range wd.Workflow {
		if len(step.Needs) == 0 {
			roots = append(roots, step)
		}
	}

	return roots
}

// GetStepByID returns a workflow step by its ID, or nil if not found.
func (wd *WorkflowDefinition) GetStepByID(id string) *WorkflowStep {
	for i := range wd.Workflow {
		if wd.Workflow[i].ID == id {
			return &wd.Workflow[i]
		}
	}
	return nil
}

// HasTemplateExpressions checks if the workflow contains template expressions like {{steps.X.outputs.Y}}
func (wd *WorkflowDefinition) HasTemplateExpressions() bool {
	for _, step := range wd.Workflow {
		// Check condition field
		if strings.Contains(step.Condition, "{{") && strings.Contains(step.Condition, "}}") {
			return true
		}

		// Check inputs
		for _, value := range step.Inputs {
			if str, ok := value.(string); ok {
				if strings.Contains(str, "{{") && strings.Contains(str, "}}") {
					return true
				}
			}
		}
	}
	return false
}
