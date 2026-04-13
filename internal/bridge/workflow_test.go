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
	"reflect"
	"strings"
	"testing"
)

func TestParseWorkflowDefinition_ValidWorkflow(t *testing.T) {
	yamlData := []byte(`
name: Feature Delivery Pipeline
workflow:
  - id: implement
    agent: autonomous-developer
    repo: pulp/pulp_python
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    outputs: [pr_url, summary]
  - id: deploy
    agent: deploy-agent
    needs: [implement]
    condition: "steps.implement.outcome == 'completed'"
    inputs:
      context: "{{steps.implement.outputs.summary}}"
  - id: verify
    agent: smoke-test
    needs: [deploy]
  - id: promote
    agent: deploy-agent
    needs: [verify]
    approval: required
`)

	wd, err := ParseWorkflowDefinition(yamlData)
	if err != nil {
		t.Fatalf("ParseWorkflowDefinition failed: %v", err)
	}

	if wd.Name != "Feature Delivery Pipeline" {
		t.Errorf("Expected name 'Feature Delivery Pipeline', got '%s'", wd.Name)
	}

	if len(wd.Workflow) != 4 {
		t.Errorf("Expected 4 workflow steps, got %d", len(wd.Workflow))
	}

	// Verify first step
	implement := wd.Workflow[0]
	if implement.ID != "implement" {
		t.Errorf("Expected step ID 'implement', got '%s'", implement.ID)
	}
	if implement.Agent != "autonomous-developer" {
		t.Errorf("Expected agent 'autonomous-developer', got '%s'", implement.Agent)
	}
	if implement.Repo != "pulp/pulp_python" {
		t.Errorf("Expected repo 'pulp/pulp_python', got '%s'", implement.Repo)
	}
	if implement.Trigger == nil || implement.Trigger.GitHub == nil {
		t.Error("Expected GitHub trigger to be set")
	}
	if len(implement.Outputs) != 2 {
		t.Errorf("Expected 2 outputs, got %d", len(implement.Outputs))
	}

	// Verify step with dependencies
	deploy := wd.Workflow[1]
	if len(deploy.Needs) != 1 || deploy.Needs[0] != "implement" {
		t.Errorf("Expected deploy step to need 'implement', got %v", deploy.Needs)
	}
	if deploy.Condition != "steps.implement.outcome == 'completed'" {
		t.Errorf("Expected specific condition, got '%s'", deploy.Condition)
	}
	if deploy.Inputs["context"] != "{{steps.implement.outputs.summary}}" {
		t.Errorf("Expected template input, got %v", deploy.Inputs["context"])
	}

	// Verify approval step
	promote := wd.Workflow[3]
	if promote.Approval != "required" {
		t.Errorf("Expected approval 'required', got '%s'", promote.Approval)
	}
}

func TestParseWorkflowDefinition_MissingName(t *testing.T) {
	yamlData := []byte(`
workflow:
  - id: test
    agent: test-agent
`)

	_, err := ParseWorkflowDefinition(yamlData)
	if err == nil {
		t.Error("Expected error for missing name, but got none")
	}
	if !strings.Contains(err.Error(), "missing required field: name") {
		t.Errorf("Expected error message about missing name, got: %s", err.Error())
	}
}

func TestParseWorkflowDefinition_EmptyWorkflow(t *testing.T) {
	yamlData := []byte(`
name: Empty Pipeline
workflow: []
`)

	_, err := ParseWorkflowDefinition(yamlData)
	if err == nil {
		t.Error("Expected error for empty workflow, but got none")
	}
	if !contains(err.Error(), "must contain at least one step") {
		t.Errorf("Expected error message about empty workflow, got: %s", err.Error())
	}
}

func TestParseWorkflowDefinition_InvalidYAML(t *testing.T) {
	yamlData := []byte(`
name: Test
workflow:
  - id: test
    agent: test-agent
  invalid yaml structure
`)

	_, err := ParseWorkflowDefinition(yamlData)
	if err == nil {
		t.Error("Expected error for invalid YAML, but got none")
	}
	if !contains(err.Error(), "parsing workflow YAML") {
		t.Errorf("Expected YAML parsing error, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_DuplicateStepIDs(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "test", Agent: "agent1"},
		{ID: "test", Agent: "agent2"},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for duplicate step IDs, but got none")
	}
	if !contains(err.Error(), "duplicate step ID: test") {
		t.Errorf("Expected error about duplicate step ID, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_MissingStepID(t *testing.T) {
	steps := []WorkflowStep{
		{Agent: "agent1"},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for missing step ID, but got none")
	}
	if !contains(err.Error(), "missing required field: id") {
		t.Errorf("Expected error about missing step ID, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_MissingAgent(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "test"},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for missing agent, but got none")
	}
	if !contains(err.Error(), "missing required field: agent") {
		t.Errorf("Expected error about missing agent, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_NonExistentDependency(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "step1", Agent: "agent1", Needs: []string{"nonexistent"}},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for non-existent dependency, but got none")
	}
	if !contains(err.Error(), "references non-existent dependency: nonexistent") {
		t.Errorf("Expected error about non-existent dependency, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_CircularDependency(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "step1", Agent: "agent1", Needs: []string{"step2"}},
		{ID: "step2", Agent: "agent2", Needs: []string{"step1"}},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for circular dependency, but got none")
	}
	if !contains(err.Error(), "circular dependency detected") {
		t.Errorf("Expected error about circular dependency, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_InvalidApproval(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "step1", Agent: "agent1", Approval: "invalid"},
	}

	err := validateWorkflowSteps(steps)
	if err == nil {
		t.Error("Expected error for invalid approval value, but got none")
	}
	if !contains(err.Error(), "invalid approval value") {
		t.Errorf("Expected error about invalid approval value, got: %s", err.Error())
	}
}

func TestValidateWorkflowSteps_ValidApproval(t *testing.T) {
	steps := []WorkflowStep{
		{ID: "step1", Agent: "agent1", Approval: "required"},
		{ID: "step2", Agent: "agent2", Approval: ""},
	}

	err := validateWorkflowSteps(steps)
	if err != nil {
		t.Errorf("Unexpected error for valid approval values: %v", err)
	}
}

func TestGetWorkflowDAG(t *testing.T) {
	wd := &WorkflowDefinition{
		Name: "Test Workflow",
		Workflow: []WorkflowStep{
			{ID: "step1", Agent: "agent1"},
			{ID: "step2", Agent: "agent2", Needs: []string{"step1"}},
			{ID: "step3", Agent: "agent3", Needs: []string{"step1", "step2"}},
		},
	}

	dag := wd.GetWorkflowDAG()

	expected := map[string][]string{
		"step1": nil,
		"step2": {"step1"},
		"step3": {"step1", "step2"},
	}

	if !reflect.DeepEqual(dag, expected) {
		t.Errorf("Expected DAG %v, got %v", expected, dag)
	}
}

func TestGetRootSteps(t *testing.T) {
	wd := &WorkflowDefinition{
		Name: "Test Workflow",
		Workflow: []WorkflowStep{
			{ID: "root1", Agent: "agent1"},
			{ID: "root2", Agent: "agent2"},
			{ID: "child", Agent: "agent3", Needs: []string{"root1"}},
		},
	}

	roots := wd.GetRootSteps()

	if len(roots) != 2 {
		t.Errorf("Expected 2 root steps, got %d", len(roots))
	}

	rootIDs := make(map[string]bool)
	for _, root := range roots {
		rootIDs[root.ID] = true
	}

	if !rootIDs["root1"] || !rootIDs["root2"] {
		t.Error("Expected root1 and root2 as root steps")
	}
}

func TestGetStepByID(t *testing.T) {
	wd := &WorkflowDefinition{
		Name: "Test Workflow",
		Workflow: []WorkflowStep{
			{ID: "step1", Agent: "agent1"},
			{ID: "step2", Agent: "agent2"},
		},
	}

	step := wd.GetStepByID("step1")
	if step == nil {
		t.Error("Expected to find step1, but got nil")
	}
	if step.Agent != "agent1" {
		t.Errorf("Expected agent1, got %s", step.Agent)
	}

	step = wd.GetStepByID("nonexistent")
	if step != nil {
		t.Error("Expected nil for nonexistent step, but got a step")
	}
}

func TestHasTemplateExpressions(t *testing.T) {
	// Workflow with template expressions
	wd1 := &WorkflowDefinition{
		Name: "Template Workflow",
		Workflow: []WorkflowStep{
			{
				ID:        "step1",
				Agent:     "agent1",
				Condition: "steps.prev.outcome == 'completed'",
			},
		},
	}

	if !wd1.HasTemplateExpressions() {
		t.Error("Expected workflow to have template expressions")
	}

	// Workflow with input templates
	wd2 := &WorkflowDefinition{
		Name: "Input Template Workflow",
		Workflow: []WorkflowStep{
			{
				ID:    "step1",
				Agent: "agent1",
				Inputs: map[string]interface{}{
					"context": "{{steps.prev.outputs.summary}}",
				},
			},
		},
	}

	if !wd2.HasTemplateExpressions() {
		t.Error("Expected workflow to have template expressions in inputs")
	}

	// Workflow without template expressions
	wd3 := &WorkflowDefinition{
		Name: "Simple Workflow",
		Workflow: []WorkflowStep{
			{
				ID:        "step1",
				Agent:     "agent1",
				Condition: "simple condition",
				Inputs: map[string]interface{}{
					"value": "simple value",
				},
			},
		},
	}

	if wd3.HasTemplateExpressions() {
		t.Error("Expected workflow to not have template expressions")
	}
}

func TestValidateAgentReference(t *testing.T) {
	ctx := context.Background()

	// Valid agent name
	err := ValidateAgentReference(ctx, "valid-agent")
	if err != nil {
		t.Errorf("Unexpected error for valid agent name: %v", err)
	}

	// Empty agent name
	err = ValidateAgentReference(ctx, "")
	if err == nil {
		t.Error("Expected error for empty agent name, but got none")
	}
}
