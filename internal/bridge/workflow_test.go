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
	"strings"
	"testing"
)

func TestParseWorkflowDefinition_Valid(t *testing.T) {
	yamlData := `
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
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wd.Name != "Feature Delivery Pipeline" {
		t.Errorf("expected name 'Feature Delivery Pipeline', got '%s'", wd.Name)
	}

	if len(wd.Workflow) != 4 {
		t.Errorf("expected 4 steps, got %d", len(wd.Workflow))
	}

	// Verify first step
	step1 := wd.Workflow[0]
	if step1.ID != "implement" {
		t.Errorf("expected step ID 'implement', got '%s'", step1.ID)
	}
	if step1.Agent != "autonomous-developer" {
		t.Errorf("expected agent 'autonomous-developer', got '%s'", step1.Agent)
	}
	if len(step1.Outputs) != 2 {
		t.Errorf("expected 2 outputs, got %d", len(step1.Outputs))
	}

	// Verify step with dependencies
	step2 := wd.Workflow[1]
	if step2.ID != "deploy" {
		t.Errorf("expected step ID 'deploy', got '%s'", step2.ID)
	}
	if len(step2.Needs) != 1 || step2.Needs[0] != "implement" {
		t.Errorf("expected needs [implement], got %v", step2.Needs)
	}
	if step2.Condition != "steps.implement.outcome == 'completed'" {
		t.Errorf("expected specific condition, got '%s'", step2.Condition)
	}

	// Verify step with approval
	step4 := wd.Workflow[3]
	if step4.Approval != "required" {
		t.Errorf("expected approval 'required', got '%s'", step4.Approval)
	}
}

func TestParseWorkflowDefinition_MissingName(t *testing.T) {
	yamlData := `
workflow:
  - id: step1
    agent: test-agent
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for missing name")
	}

	expectedMsg := "workflow definition missing required field: name"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_EmptyWorkflow(t *testing.T) {
	yamlData := `
name: Empty Workflow
workflow: []
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for empty workflow")
	}

	expectedMsg := "workflow definition must contain at least one step"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_MissingStepID(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - agent: test-agent
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for missing step ID")
	}

	expectedMsg := "workflow step missing required field: id"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_MissingStepAgent(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for missing step agent")
	}

	expectedMsg := "workflow step 'step1' missing required field: agent"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_DuplicateStepIDs(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: agent1
  - id: step1
    agent: agent2
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for duplicate step IDs")
	}

	expectedMsg := "duplicate step ID: step1"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_InvalidApproval(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: test-agent
    approval: invalid
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for invalid approval value")
	}

	expectedMsg := "workflow step 'step1' has invalid approval value 'invalid'"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_NonexistentDependency(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: agent1
    needs: [nonexistent]
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for non-existent dependency")
	}

	expectedMsg := "workflow step 'step1' references non-existent dependency: nonexistent"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_CircularDependency(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: agent1
    needs: [step2]
  - id: step2
    agent: agent2
    needs: [step1]
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}

	expectedMsg := "circular dependency detected in workflow"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestParseWorkflowDefinition_ComplexCircularDependency(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: agent1
    needs: [step2]
  - id: step2
    agent: agent2
    needs: [step3]
  - id: step3
    agent: agent3
    needs: [step1]
`

	_, err := ParseWorkflowDefinition([]byte(yamlData))
	if err == nil {
		t.Fatal("expected error for complex circular dependency")
	}

	expectedMsg := "circular dependency detected in workflow"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message to contain '%s', got '%s'", expectedMsg, err.Error())
	}
}

func TestWorkflowDefinition_GetRootSteps(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: root1
    agent: agent1
  - id: root2
    agent: agent2
  - id: child1
    agent: agent3
    needs: [root1]
  - id: child2
    agent: agent4
    needs: [root1, root2]
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	roots := wd.GetRootSteps()
	if len(roots) != 2 {
		t.Errorf("expected 2 root steps, got %d", len(roots))
	}

	rootIDs := make(map[string]bool)
	for _, step := range roots {
		rootIDs[step.ID] = true
	}

	if !rootIDs["root1"] || !rootIDs["root2"] {
		t.Errorf("expected root1 and root2, got %v", rootIDs)
	}
}

func TestWorkflowDefinition_GetStepByID(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: agent1
  - id: step2
    agent: agent2
    needs: [step1]
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	step := wd.GetStepByID("step1")
	if step == nil {
		t.Fatal("expected to find step1")
	}
	if step.Agent != "agent1" {
		t.Errorf("expected agent1, got '%s'", step.Agent)
	}

	step = wd.GetStepByID("nonexistent")
	if step != nil {
		t.Error("expected nil for nonexistent step")
	}
}

func TestWorkflowDefinition_GetDependents(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: root
    agent: agent1
  - id: child1
    agent: agent2
    needs: [root]
  - id: child2
    agent: agent3
    needs: [root]
  - id: grandchild
    agent: agent4
    needs: [child1]
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dependents := wd.GetDependents("root")
	if len(dependents) != 2 {
		t.Errorf("expected 2 dependents of root, got %d", len(dependents))
	}

	dependentIDs := make(map[string]bool)
	for _, step := range dependents {
		dependentIDs[step.ID] = true
	}

	if !dependentIDs["child1"] || !dependentIDs["child2"] {
		t.Errorf("expected child1 and child2, got %v", dependentIDs)
	}

	// Test step with no dependents
	dependents = wd.GetDependents("grandchild")
	if len(dependents) != 0 {
		t.Errorf("expected 0 dependents of grandchild, got %d", len(dependents))
	}
}

func TestValidateConditionSyntax(t *testing.T) {
	tests := []struct {
		condition string
		valid     bool
	}{
		{"steps.implement.outcome == 'completed'", true},
		{"steps.test.outputs.status == 'success'", true},
		{"true", true},
		{"false", true},
		{"some custom condition", false}, // Now rejecting invalid syntax
		{"", true},                       // Empty is valid
	}

	for _, test := range tests {
		err := validateConditionSyntax(test.condition)
		if test.valid && err != nil {
			t.Errorf("condition '%s' should be valid but got error: %v", test.condition, err)
		}
		if !test.valid && err == nil {
			t.Errorf("condition '%s' should be invalid but no error occurred", test.condition)
		}
	}
}

func TestValidateInputsTemplateSyntax(t *testing.T) {
	tests := []struct {
		inputs map[string]interface{}
		valid  bool
		desc   string
	}{
		{
			inputs: map[string]interface{}{
				"context": "{{steps.implement.outputs.summary}}",
			},
			valid: true,
			desc:  "valid template syntax",
		},
		{
			inputs: map[string]interface{}{
				"simple": "just a string",
			},
			valid: true,
			desc:  "simple string without template",
		},
		{
			inputs: map[string]interface{}{
				"number": 42,
			},
			valid: true,
			desc:  "non-string value",
		},
		{
			inputs: map[string]interface{}{
				"invalid": "{{invalid.template}}",
			},
			valid: false,
			desc:  "invalid template without steps prefix",
		},
		{
			inputs: map[string]interface{}{},
			valid:  true,
			desc:   "empty inputs",
		},
	}

	for _, test := range tests {
		err := validateInputsTemplateSyntax(test.inputs)
		if test.valid && err != nil {
			t.Errorf("inputs should be valid (%s) but got error: %v", test.desc, err)
		}
		if !test.valid && err == nil {
			t.Errorf("inputs should be invalid (%s) but no error occurred", test.desc)
		}
	}
}

func TestParseRepoService(t *testing.T) {
	tests := []struct {
		repoURL         string
		expectedService string
		expectError     bool
	}{
		{"github.com/owner/repo", "github", false},
		{"https://github.com/owner/repo", "github", false},
		{"git@github.com:owner/repo.git", "github", false},
		{"gitlab.com/owner/repo", "gitlab", false},
		{"https://gitlab.example.com/owner/repo", "gitlab", false},
		{"bitbucket.org/owner/repo", "bitbucket", false},
		{"https://git.mycompany.com/owner/repo", "github", false}, // Default to github for unknown
		{"invalid-url", "github", false},                          // Default to github
	}

	for _, test := range tests {
		service, err := parseRepoService(test.repoURL)
		if test.expectError && err == nil {
			t.Errorf("expected error for URL '%s' but got none", test.repoURL)
		}
		if !test.expectError && err != nil {
			t.Errorf("unexpected error for URL '%s': %v", test.repoURL, err)
		}
		if service != test.expectedService {
			t.Errorf("URL '%s': expected service '%s', got '%s'", test.repoURL, test.expectedService, service)
		}
	}
}

func TestParseWorkflowDefinition_CrossRepoValidation(t *testing.T) {
	yamlData := `
name: Cross-Repo Workflow
workflow:
  - id: implement-plugin
    agent: autonomous-developer
    repo: pulp/pulp_python
  - id: implement-core
    agent: autonomous-developer
    repo: pulp/pulpcore
    needs: [implement-plugin]
  - id: integration-test
    agent: test-runner
    repo: pulp/pulp_integration
    needs: [implement-core]
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error parsing cross-repo workflow: %v", err)
	}

	if wd.Name != "Cross-Repo Workflow" {
		t.Errorf("expected name 'Cross-Repo Workflow', got '%s'", wd.Name)
	}

	if len(wd.Workflow) != 3 {
		t.Errorf("expected 3 workflow steps, got %d", len(wd.Workflow))
	}

	// Check each step has the correct repo
	expectedRepos := map[string]string{
		"implement-plugin": "pulp/pulp_python",
		"implement-core":   "pulp/pulpcore",
		"integration-test": "pulp/pulp_integration",
	}

	for _, step := range wd.Workflow {
		expectedRepo := expectedRepos[step.ID]
		if step.Repo != expectedRepo {
			t.Errorf("step '%s': expected repo '%s', got '%s'", step.ID, expectedRepo, step.Repo)
		}
	}

	// Verify dependencies are preserved
	coreStep := wd.GetStepByID("implement-core")
	if coreStep == nil || len(coreStep.Needs) != 1 || coreStep.Needs[0] != "implement-plugin" {
		t.Error("implement-core step should depend on implement-plugin")
	}

	testStep := wd.GetStepByID("integration-test")
	if testStep == nil || len(testStep.Needs) != 1 || testStep.Needs[0] != "implement-core" {
		t.Error("integration-test step should depend on implement-core")
	}
}

func TestParseWorkflowDefinition_FieldBasedRouting(t *testing.T) {
	yamlData := `
name: Field-Based Routing Workflow
workflow:
  - id: triage
    agent: triage-agent
    route_field: is_clear
    route_map:
      "true": planning
      "false": manual-review
    outputs: [triage_result, is_clear]

  - id: planning
    agent: planning-agent
    needs: [triage]

  - id: manual-review
    agent: manual-review-agent
    needs: [triage]
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error parsing field-based routing workflow: %v", err)
	}

	if wd.Name != "Field-Based Routing Workflow" {
		t.Errorf("expected name 'Field-Based Routing Workflow', got '%s'", wd.Name)
	}

	if len(wd.Workflow) != 3 {
		t.Errorf("expected 3 workflow steps, got %d", len(wd.Workflow))
	}

	// Check the triage step has routing configuration
	triageStep := wd.GetStepByID("triage")
	if triageStep == nil {
		t.Fatal("triage step not found")
	}

	if triageStep.RouteField != "is_clear" {
		t.Errorf("expected route_field 'is_clear', got '%s'", triageStep.RouteField)
	}

	expectedRouteMap := map[string]string{
		"true":  "planning",
		"false": "manual-review",
	}

	if len(triageStep.RouteMap) != len(expectedRouteMap) {
		t.Errorf("expected route_map with %d entries, got %d", len(expectedRouteMap), len(triageStep.RouteMap))
	}

	for key, expectedValue := range expectedRouteMap {
		if actualValue, exists := triageStep.RouteMap[key]; !exists {
			t.Errorf("expected route_map key '%s' not found", key)
		} else if actualValue != expectedValue {
			t.Errorf("route_map['%s']: expected '%s', got '%s'", key, expectedValue, actualValue)
		}
	}
}

func TestParseWorkflowDefinition_InvalidRouting(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		errorMsg string
	}{
		{
			name: "route_field without route_map",
			yaml: `
name: Invalid Routing
workflow:
  - id: step1
    agent: test-agent
    route_field: status
`,
			errorMsg: "route_field specified but route_map is empty",
		},
		{
			name: "route_map without route_field",
			yaml: `
name: Invalid Routing
workflow:
  - id: step1
    agent: test-agent
    route_map:
      success: step2
`,
			errorMsg: "route_map specified but route_field is empty",
		},
		{
			name: "empty route_field",
			yaml: `
name: Invalid Routing
workflow:
  - id: step1
    agent: test-agent
    route_field: ""
    route_map:
      success: step2
`,
			errorMsg: "route_map specified but route_field is empty",
		},
		{
			name: "empty route_map value",
			yaml: `
name: Invalid Routing
workflow:
  - id: step1
    agent: test-agent
    route_field: status
    route_map:
      success: ""
`,
			errorMsg: "empty value in route_map",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseWorkflowDefinition([]byte(test.yaml))
			if err == nil {
				t.Fatalf("expected error for %s", test.name)
			}

			if !strings.Contains(err.Error(), test.errorMsg) {
				t.Errorf("expected error message to contain '%s', got '%s'", test.errorMsg, err.Error())
			}
		})
	}
}

func TestValidateRouteConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		routeField string
		routeMap   map[string]string
		hasError   bool
	}{
		{
			name:       "valid configuration",
			routeField: "status",
			routeMap:   map[string]string{"success": "next_step", "fail": "error_step"},
			hasError:   false,
		},
		{
			name:       "empty configuration",
			routeField: "",
			routeMap:   map[string]string{},
			hasError:   false,
		},
		{
			name:       "field without map",
			routeField: "status",
			routeMap:   map[string]string{},
			hasError:   true,
		},
		{
			name:       "map without field",
			routeField: "",
			routeMap:   map[string]string{"success": "next_step"},
			hasError:   true,
		},
		{
			name:       "invalid field name",
			routeField: "123invalid",
			routeMap:   map[string]string{"success": "next_step"},
			hasError:   true,
		},
		{
			name:       "empty map value",
			routeField: "status",
			routeMap:   map[string]string{"success": ""},
			hasError:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRouteConfiguration(test.routeField, test.routeMap)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestIsValidIdentifier(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"simple identifier", "status", true},
		{"underscore prefix", "_private", true},
		{"mixed case", "MyField", true},
		{"with numbers", "field_123", true},
		{"empty string", "", false},
		{"starts with number", "123invalid", false},
		{"with spaces", "my field", false},
		{"with hyphens", "my-field", false},
		{"with dots", "field.name", false},
		{"single underscore", "_", true},
		{"just letters", "abc", true},
		{"just numbers after letter", "a123", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := isValidIdentifier(test.input)
			if result != test.valid {
				t.Errorf("isValidIdentifier('%s'): expected %v, got %v", test.input, test.valid, result)
			}
		})
	}
}

func TestParseWorkflowWithStepCredentials(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: Test Agent
    credentials:
      API_KEY: my-secret
      DB_PASSWORD: db-cred
`
	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(wd.Workflow) != 1 {
		t.Fatalf("expected 1 step, got %d", len(wd.Workflow))
	}
	step := wd.Workflow[0]
	if len(step.Credentials) != 2 {
		t.Errorf("expected 2 credentials, got %d", len(step.Credentials))
	}
	if step.Credentials["API_KEY"] != "my-secret" {
		t.Errorf("API_KEY: got %q, want %q", step.Credentials["API_KEY"], "my-secret")
	}
	if step.Credentials["DB_PASSWORD"] != "db-cred" {
		t.Errorf("DB_PASSWORD: got %q, want %q", step.Credentials["DB_PASSWORD"], "db-cred")
	}
}

func TestParseWorkflowWithoutStepCredentials(t *testing.T) {
	yamlData := `
name: Test Workflow
workflow:
  - id: step1
    agent: Test Agent
`
	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	step := wd.Workflow[0]
	if len(step.Credentials) != 0 {
		t.Errorf("expected 0 credentials, got %d", len(step.Credentials))
	}
}

func TestParseWorkflowWithDirectOutbound(t *testing.T) {
	yamlData := `
name: Test
workflow:
  - id: step1
    agent: Test Agent
    direct_outbound: true
`
	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wd.Workflow[0].DirectOutbound {
		t.Error("expected DirectOutbound=true")
	}
}

func TestParseWorkflowWithDirectOutboundFalse(t *testing.T) {
	yamlData := `
name: Test
workflow:
  - id: step1
    agent: Test Agent
`
	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wd.Workflow[0].DirectOutbound {
		t.Error("expected DirectOutbound=false by default")
	}
}

// TestParseWorkflowDefinition_SDLCPipeline parses a complete SDLC pipeline YAML
// with bridge actions, depends expressions, max_iterations, and various step
// types to verify the parsed structure.
func TestParseWorkflowDefinition_SDLCPipeline(t *testing.T) {
	yamlData := `
name: SDLC Pipeline
workflow:
  - id: implement
    agent: autonomous-developer
    repo: pulp/pulp_python
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    outputs: [pr_url, summary, branch]

  - id: create-pr
    type: bridge
    action: create-pr
    needs: [implement]
    inputs:
      repo: pulp/pulp_python
      branch: "{{steps.implement.inputs.branch}}"
      title: "Fix issue {{trigger.issue_number}}"
    outputs: [pr_number, pr_url]

  - id: await-ci
    type: bridge
    action: await-ci
    needs: [create-pr]
    inputs:
      repo: pulp/pulp_python
      pr_number: "{{steps.create-pr.outputs.pr_number}}"
    outputs: [ci_status]

  - id: review
    agent: code-reviewer
    depends: "await-ci.completed"
    max_iterations: 3
    inputs:
      pr_url: "{{steps.create-pr.outputs.pr_url}}"

  - id: merge-pr
    type: bridge
    action: merge-pr
    depends: "review.completed"
    inputs:
      repo: pulp/pulp_python
      pr_number: "{{steps.create-pr.outputs.pr_number}}"
`

	wd, err := ParseWorkflowDefinition([]byte(yamlData))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wd.Name != "SDLC Pipeline" {
		t.Errorf("expected name 'SDLC Pipeline', got '%s'", wd.Name)
	}
	if len(wd.Workflow) != 5 {
		t.Fatalf("expected 5 steps, got %d", len(wd.Workflow))
	}

	// Step 1: implement (agent step with trigger)
	s := wd.Workflow[0]
	if s.ID != "implement" {
		t.Errorf("step 0: expected ID 'implement', got '%s'", s.ID)
	}
	if s.Agent != "autonomous-developer" {
		t.Errorf("step 0: expected agent 'autonomous-developer', got '%s'", s.Agent)
	}
	if s.Trigger == nil || s.Trigger.GitHub == nil {
		t.Fatal("step 0: expected GitHub trigger")
	}
	if len(s.Trigger.GitHub.Events) != 1 || s.Trigger.GitHub.Events[0] != "issues" {
		t.Errorf("step 0: expected events [issues], got %v", s.Trigger.GitHub.Events)
	}
	if len(s.Outputs) != 3 {
		t.Errorf("step 0: expected 3 outputs, got %d", len(s.Outputs))
	}

	// Step 2: create-pr (bridge step with needs)
	s = wd.Workflow[1]
	if s.ID != "create-pr" {
		t.Errorf("step 1: expected ID 'create-pr', got '%s'", s.ID)
	}
	if s.Type != "bridge" {
		t.Errorf("step 1: expected type 'bridge', got '%s'", s.Type)
	}
	if s.Action != "create-pr" {
		t.Errorf("step 1: expected action 'create-pr', got '%s'", s.Action)
	}
	if len(s.Needs) != 1 || s.Needs[0] != "implement" {
		t.Errorf("step 1: expected needs [implement], got %v", s.Needs)
	}
	if len(s.Inputs) != 3 {
		t.Errorf("step 1: expected 3 inputs, got %d", len(s.Inputs))
	}

	// Step 3: await-ci (bridge step)
	s = wd.Workflow[2]
	if s.ID != "await-ci" {
		t.Errorf("step 2: expected ID 'await-ci', got '%s'", s.ID)
	}
	if s.Type != "bridge" || s.Action != "await-ci" {
		t.Errorf("step 2: expected bridge/await-ci, got %s/%s", s.Type, s.Action)
	}

	// Step 4: review (agent step with depends and max_iterations)
	s = wd.Workflow[3]
	if s.ID != "review" {
		t.Errorf("step 3: expected ID 'review', got '%s'", s.ID)
	}
	if s.Type != "" {
		t.Errorf("step 3: expected empty type (defaults to agent), got '%s'", s.Type)
	}
	if s.Depends != "await-ci.completed" {
		t.Errorf("step 3: expected depends 'await-ci.completed', got '%s'", s.Depends)
	}
	if s.MaxIterations != 3 {
		t.Errorf("step 3: expected max_iterations 3, got %d", s.MaxIterations)
	}

	// Step 5: merge-pr (bridge step with depends)
	s = wd.Workflow[4]
	if s.ID != "merge-pr" {
		t.Errorf("step 4: expected ID 'merge-pr', got '%s'", s.ID)
	}
	if s.Type != "bridge" || s.Action != "merge-pr" {
		t.Errorf("step 4: expected bridge/merge-pr, got %s/%s", s.Type, s.Action)
	}
	if s.Depends != "review.completed" {
		t.Errorf("step 4: expected depends 'review.completed', got '%s'", s.Depends)
	}
}

func TestValidateConditionSyntax_Enhanced(t *testing.T) {
	tests := []struct {
		condition string
		valid     bool
		desc      string
	}{
		{"steps.implement.outcome == 'completed'", true, "basic outcome condition"},
		{"steps.test.outputs.status == 'success'", true, "basic output condition"},
		{"steps.test.outputs.coverage >= 80", true, "numeric comparison"},
		{"steps.a.outcome == 'completed' && steps.b.outcome == 'completed'", true, "AND condition"},
		{"steps.a.outcome == 'completed' || steps.b.outcome == 'failed'", true, "OR condition"},
		{"steps.test.outputs.coverage > 80 && steps.test.outcome == 'completed'", true, "mixed conditions"},
		{"true", true, "boolean literal true"},
		{"false", true, "boolean literal false"},
		{"", true, "empty condition"},
		{"invalid.format", false, "invalid syntax"},
		{"steps.test.outcome === 'completed'", false, "invalid operator"},
		{"steps.test.outcome == completed", false, "missing quotes"},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := validateConditionSyntax(test.condition)
			if test.valid && err != nil {
				t.Errorf("condition '%s' should be valid but got error: %v", test.condition, err)
			}
			if !test.valid && err == nil {
				t.Errorf("condition '%s' should be invalid but no error occurred", test.condition)
			}
		})
	}
}
