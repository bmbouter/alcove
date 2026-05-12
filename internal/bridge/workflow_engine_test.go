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
	"testing"
	"time"
)

// TestExpandTemplateWithContext verifies template expansion in workflow inputs,
// covering trigger references, hyphenated step IDs, input prefix lookups, and
// non-string value conversion.
func TestExpandTemplateWithContext(t *testing.T) {
	// expandTemplateWithContext is a method on *WorkflowEngine but does not
	// touch the database, so a zero-value engine with nil deps is fine.
	we := &WorkflowEngine{}

	// Build step outputs that simulate previous step completions.
	stepOutputs := map[string]interface{}{
		// Step with a simple ID.
		"implement": map[string]interface{}{
			"summary":        "Implemented the feature",
			"_input_branch":  "feature/issue-42",
		},
		// Step with a hyphenated ID (the regex must use [\w-]+).
		"create-pr": map[string]interface{}{
			"pr_number": float64(99), // JSON numbers decode as float64
			"pr_url":    "https://github.com/org/repo/pull/99",
		},
	}

	triggerRef := "owner/repo#42"

	tests := []struct {
		name     string
		template string
		expected string
	}{
		{
			name:     "trigger issue_number from triggerRef",
			template: "Fix issue {{trigger.issue_number}}",
			expected: "Fix issue 42",
		},
		{
			name:     "hyphenated step ID in outputs",
			template: "PR #{{steps.create-pr.outputs.pr_number}}",
			expected: "PR #99",
		},
		{
			name:     "input prefix lookup via steps.X.inputs.Y",
			template: "Branch: {{steps.implement.inputs.branch}}",
			expected: "Branch: feature/issue-42",
		},
		{
			name:     "regular output expansion",
			template: "Summary: {{steps.implement.outputs.summary}}",
			expected: "Summary: Implemented the feature",
		},
		{
			name:     "non-string float64 converted to string",
			template: "{{steps.create-pr.outputs.pr_number}}",
			expected: "99",
		},
		{
			name:     "unresolved template remains as literal",
			template: "{{steps.nonexistent.outputs.value}}",
			expected: "{{steps.nonexistent.outputs.value}}",
		},
		{
			name:     "multiple templates in one string",
			template: "PR {{steps.create-pr.outputs.pr_number}} for issue {{trigger.issue_number}}",
			expected: "PR 99 for issue 42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := we.expandTemplateWithContext(tt.template, stepOutputs, triggerRef)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestExpandTemplateWithContext_IntValue ensures integer values stored in step
// outputs (as opposed to float64 from JSON) are also converted to strings.
func TestExpandTemplateWithContext_IntValue(t *testing.T) {
	we := &WorkflowEngine{}

	stepOutputs := map[string]interface{}{
		"build": map[string]interface{}{
			"exit_code": 0, // plain int, not float64
		},
	}

	result, err := we.expandTemplateWithContext("{{steps.build.outputs.exit_code}}", stepOutputs, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "0" {
		t.Errorf("got %q, want %q", result, "0")
	}
}

// TestExpandTemplateWithContext_EmptyTriggerRef ensures that trigger templates
// resolve to empty string (not panic) when triggerRef has no "#" delimiter.
func TestExpandTemplateWithContext_EmptyTriggerRef(t *testing.T) {
	we := &WorkflowEngine{}

	result, err := we.expandTemplateWithContext("Issue {{trigger.issue_number}}", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Issue " {
		t.Errorf("got %q, want %q", result, "Issue ")
	}
}

// TestExpandTemplateWithContext_NilStepOutputs ensures that step template
// references remain as literals when stepOutputs is nil (not panic).
func TestExpandTemplateWithContext_NilStepOutputs(t *testing.T) {
	we := &WorkflowEngine{}

	result, err := we.expandTemplateWithContext("{{steps.build.outputs.status}}", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "{{steps.build.outputs.status}}" {
		t.Errorf("got %q, want %q", result, "{{steps.build.outputs.status}}")
	}
}

// TestCancelWorkflowRunValidation tests the validation logic for cancelling workflow runs.
// This is a unit test that focuses on the validation part without requiring database interactions.
func TestCancelWorkflowRunValidation(t *testing.T) {
	// This test would validate the business logic for determining if a workflow run
	// can be cancelled based on its status. Since the actual implementation requires
	// database access, this serves as documentation of the expected behavior:

	// - Should allow cancellation of "pending", "running", "awaiting_approval" status
	// - Should reject cancellation of "completed", "failed", "cancelled" status
	// - Should cancel all pending/running/awaiting_approval steps
	// - Should attempt to cancel associated sessions

	validStatuses := []string{"pending", "running", "awaiting_approval"}
	invalidStatuses := []string{"completed", "failed", "cancelled"}

	for _, status := range validStatuses {
		t.Logf("Status %s should be cancellable", status)
	}

	for _, status := range invalidStatuses {
		t.Logf("Status %s should not be cancellable", status)
	}
}

func TestParseSinceParam(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		expected time.Duration // approximate duration from now
	}{
		{"empty", "", false, 0},
		{"1 day", "1d", false, -24 * time.Hour},
		{"7 days", "7d", false, -7 * 24 * time.Hour},
		{"30 days", "30d", false, -30 * 24 * time.Hour},
		{"ISO date", "2023-01-01T00:00:00Z", false, 0},
		{"date only", "2023-01-01", false, 0},
		{"invalid", "invalid", true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSinceParam(tt.input)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.input == "" {
				if result != nil {
					t.Errorf("expected nil for empty input, got %v", result)
				}
				return
			}

			if result == nil {
				t.Errorf("expected non-nil result for input %s", tt.input)
				return
			}

			// For relative dates, check they are approximately correct
			if tt.expected != 0 {
				now := time.Now()
				expectedTime := now.Add(tt.expected)
				diff := expectedTime.Sub(*result)
				if diff > time.Minute || diff < -time.Minute {
					t.Errorf("time difference too large: expected around %v, got %v (diff: %v)",
						expectedTime, *result, diff)
				}
			}
		})
	}
}

func TestWorkflowRunsFilter_validate(t *testing.T) {
	tests := []struct {
		name    string
		filter  WorkflowRunsFilter
		wantErr bool
		checks  func(*testing.T, *WorkflowRunsFilter)
	}{
		{
			name:    "missing team ID",
			filter:  WorkflowRunsFilter{},
			wantErr: true,
		},
		{
			name: "default limit applied",
			filter: WorkflowRunsFilter{
				TeamID: "team-1",
				Limit:  0,
			},
			wantErr: false,
			checks: func(t *testing.T, f *WorkflowRunsFilter) {
				if f.Limit != 25 {
					t.Errorf("expected default limit 25, got %d", f.Limit)
				}
			},
		},
		{
			name: "limit too high",
			filter: WorkflowRunsFilter{
				TeamID: "team-1",
				Limit:  500,
			},
			wantErr: false,
			checks: func(t *testing.T, f *WorkflowRunsFilter) {
				if f.Limit != 200 {
					t.Errorf("expected capped limit 200, got %d", f.Limit)
				}
			},
		},
		{
			name: "negative offset corrected",
			filter: WorkflowRunsFilter{
				TeamID: "team-1",
				Offset: -5,
			},
			wantErr: false,
			checks: func(t *testing.T, f *WorkflowRunsFilter) {
				if f.Offset != 0 {
					t.Errorf("expected corrected offset 0, got %d", f.Offset)
				}
			},
		},
		{
			name: "valid since parameter",
			filter: WorkflowRunsFilter{
				TeamID: "team-1",
				Since:  "7d",
			},
			wantErr: false,
			checks: func(t *testing.T, f *WorkflowRunsFilter) {
				if f.SinceTime == nil {
					t.Errorf("expected SinceTime to be set")
				}
			},
		},
		{
			name: "invalid since parameter",
			filter: WorkflowRunsFilter{
				TeamID: "team-1",
				Since:  "invalid",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filter.validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if tt.checks != nil {
				tt.checks(t, &tt.filter)
			}
		})
	}
}

// TestCIFixDispatchDependency verifies that ci-fix steps correctly dispatch
// when await-ci steps fail (not when they succeed).
func TestCIFixDispatchDependency(t *testing.T) {
	tests := []struct {
		name             string
		awaitCIStatus    string
		codeReviewStatus string
		expectedCIFix    bool
		expectedReview   bool
	}{
		{
			name:             "CI fails - ci-fix should dispatch",
			awaitCIStatus:    "failed",
			codeReviewStatus: "pending",
			expectedCIFix:    true,
			expectedReview:   false,
		},
		{
			name:             "CI passes - code-review should dispatch",
			awaitCIStatus:    "completed",
			codeReviewStatus: "pending",
			expectedCIFix:    false,
			expectedReview:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stepStatuses := map[string]string{
				"await-ci":     tt.awaitCIStatus,
				"code-review":  tt.codeReviewStatus,
			}

			// Test ci-fix dependency: depends on "await-ci.Failed"
			cifixReady, err := EvaluateDepends("await-ci.Failed", stepStatuses)
			if err != nil {
				t.Fatalf("EvaluateDepends for ci-fix failed: %v", err)
			}
			if cifixReady != tt.expectedCIFix {
				t.Errorf("ci-fix readiness: got %v, expected %v", cifixReady, tt.expectedCIFix)
			}

			// Test code-review dependency: depends on "await-ci.Succeeded"
			reviewReady, err := EvaluateDepends("await-ci.Succeeded", stepStatuses)
			if err != nil {
				t.Fatalf("EvaluateDepends for code-review failed: %v", err)
			}
			if reviewReady != tt.expectedReview {
				t.Errorf("code-review readiness: got %v, expected %v", reviewReady, tt.expectedReview)
			}
		})
	}
}

func TestValidateStepOutputs(t *testing.T) {
	we := &WorkflowEngine{}

	tests := []struct {
		name      string
		contract  *OutputContract
		outputs   map[string]interface{}
		expectErr bool
		errMsg    string
	}{
		{
			name: "valid outputs with all required fields",
			contract: &OutputContract{
				Required: []string{"verdict", "fixes_required"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict":        "pass",
				"fixes_required": []string{"fix1", "fix2"},
			},
			expectErr: false,
		},
		{
			name: "missing required field",
			contract: &OutputContract{
				Required: []string{"verdict", "fixes_required"},
			},
			outputs: map[string]interface{}{
				"verdict": "pass",
			},
			expectErr: true,
			errMsg:    "required output field 'fixes_required' is missing",
		},
		{
			name: "invalid value not in allowed_values",
			contract: &OutputContract{
				Required: []string{"verdict"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict": "unknown",
			},
			expectErr: true,
			errMsg:    "allowed: [pass fail]",
		},
		{
			name: "valid with extra fields not in contract",
			contract: &OutputContract{
				Required: []string{"verdict"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict":    "pass",
				"extra_info": "some additional data",
			},
			expectErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			valid, reason := we.validateOutputContract(test.contract, test.outputs)

			if test.expectErr {
				if valid {
					t.Fatalf("expected validation to fail but it passed")
				}
				if !stringContains(reason, test.errMsg) {
					t.Errorf("error reason should contain '%s', got: %s", test.errMsg, reason)
				}
			} else {
				if !valid {
					t.Fatalf("expected validation to pass but it failed: %s", reason)
				}
			}
		})
	}
}

// TestWorkflowEngineValidateOutputContract tests the output contract validation logic in the workflow engine.
func TestWorkflowEngineValidateOutputContract(t *testing.T) {
	we := &WorkflowEngine{}

	tests := []struct {
		name      string
		contract  *OutputContract
		outputs   map[string]interface{}
		expectErr bool
		errMsg    string
	}{
		{
			name: "contract satisfied - all required fields present",
			contract: &OutputContract{
				Required: []string{"verdict", "fixes_required"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict":        "pass",
				"fixes_required": true,
			},
			expectErr: false,
		},
		{
			name: "missing required field",
			contract: &OutputContract{
				Required: []string{"verdict", "fixes_required"},
			},
			outputs: map[string]interface{}{
				"verdict": "pass",
				// fixes_required is missing
			},
			expectErr: true,
			errMsg:    "required output field 'fixes_required' is missing",
		},
		{
			name: "nil value for required field",
			contract: &OutputContract{
				Required: []string{"verdict"},
			},
			outputs: map[string]interface{}{
				"verdict": nil,
			},
			expectErr: true,
			errMsg:    "required output field 'verdict' is missing",
		},
		{
			name: "invalid allowed value",
			contract: &OutputContract{
				Required: []string{"verdict"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict": "maybe",
			},
			expectErr: true,
			errMsg:    "output field 'verdict' has value 'maybe', allowed: [pass fail]",
		},
		{
			name: "valid allowed value",
			contract: &OutputContract{
				Required: []string{"verdict"},
				AllowedValues: map[string][]string{
					"verdict": {"pass", "fail"},
				},
			},
			outputs: map[string]interface{}{
				"verdict": "fail",
			},
			expectErr: false,
		},
		{
			name: "allowed values check skips non-existent fields",
			contract: &OutputContract{
				AllowedValues: map[string][]string{
					"optional_field": {"value1", "value2"},
				},
			},
			outputs: map[string]interface{}{
				// optional_field is not present, should not fail
			},
			expectErr: false,
		},
		{
			name: "empty outputs with contract",
			contract: &OutputContract{
				Required: []string{"verdict"},
			},
			outputs:   map[string]interface{}{},
			expectErr: true,
			errMsg:    "required output field 'verdict' is missing",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			valid, reason := we.validateOutputContract(test.contract, test.outputs)

			if test.expectErr {
				if valid {
					t.Fatalf("expected validation to fail but it passed")
				}
				if !stringContains(reason, test.errMsg) {
					t.Errorf("error reason should contain '%s', got: %s", test.errMsg, reason)
				}
			} else {
				if !valid {
					t.Fatalf("expected validation to pass but it failed: %s", reason)
				}
			}
		})
	}
}

// stringContains is a helper to check substring presence
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
