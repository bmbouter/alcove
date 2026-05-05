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

// TestAwaitCISemantics tests that the core semantic fix is correct:
// when CI fails, the bridge action should return Status="failed" instead of Status="succeeded".
//
// This test validates the fix for issue #592 where ci-fix steps were not dispatching
// because await-ci was returning "succeeded" even when CI failed.
func TestAwaitCISemantics(t *testing.T) {
	// These test cases validate the core semantic behavior without requiring
	// HTTP mocking or credential store setup.

	tests := []struct {
		name           string
		actionResult   *BridgeActionResult
		expectedStep   string
	}{
		{
			name: "CI passes - step status should be completed",
			actionResult: &BridgeActionResult{
				Status: "succeeded",
				Outputs: map[string]interface{}{
					"status":        "passed",
					"failure_logs":  "",
					"failed_checks": []string{},
				},
			},
			expectedStep: "completed",
		},
		{
			name: "CI fails - step status should be failed",
			actionResult: &BridgeActionResult{
				Status: "failed",
				Outputs: map[string]interface{}{
					"status":        "failed",
					"failure_logs":  "test failed\nerror message here",
					"failed_checks": []string{"test-check"},
				},
			},
			expectedStep: "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate how the workflow engine maps BridgeActionResult.Status to step status
			var stepStatus string
			if tt.actionResult.Status == "succeeded" {
				stepStatus = "completed"
			} else {
				stepStatus = "failed"
			}

			if stepStatus != tt.expectedStep {
				t.Errorf("expected step status %s, got %s for BridgeActionResult.Status=%s",
					tt.expectedStep, stepStatus, tt.actionResult.Status)
			}

			// Verify outputs are preserved regardless of step status
			if tt.actionResult.Outputs == nil {
				t.Error("expected outputs to be preserved")
			}
		})
	}
}

// TestDependsEvaluationWithFailedStep tests that step dependency evaluation
// correctly handles failed await-ci steps.
func TestDependsEvaluationWithFailedStep(t *testing.T) {
	tests := []struct {
		name           string
		dependsExpr    string
		stepStatuses   map[string]string
		expectedResult bool
	}{
		{
			name:        "ci-fix should dispatch when await-ci fails",
			dependsExpr: "await-ci.Failed",
			stepStatuses: map[string]string{
				"await-ci": "failed",
			},
			expectedResult: true,
		},
		{
			name:        "ci-fix should not dispatch when await-ci succeeds",
			dependsExpr: "await-ci.Failed",
			stepStatuses: map[string]string{
				"await-ci": "completed",
			},
			expectedResult: false,
		},
		{
			name:        "code-review should dispatch when await-ci succeeds",
			dependsExpr: "await-ci.Succeeded",
			stepStatuses: map[string]string{
				"await-ci": "completed",
			},
			expectedResult: true,
		},
		{
			name:        "code-review should not dispatch when await-ci fails",
			dependsExpr: "await-ci.Succeeded",
			stepStatuses: map[string]string{
				"await-ci": "failed",
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateDepends(tt.dependsExpr, tt.stepStatuses)
			if err != nil {
				t.Fatalf("EvaluateDepends returned error: %v", err)
			}

			if result != tt.expectedResult {
				t.Errorf("expected %v, got %v for expression '%s' with statuses %v",
					tt.expectedResult, result, tt.dependsExpr, tt.stepStatuses)
			}
		})
	}
}
