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
