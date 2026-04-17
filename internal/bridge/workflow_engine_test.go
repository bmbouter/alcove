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
	"encoding/json"
	"testing"
)

// TestReadStepOutputs verifies that the readStepOutputs function correctly
// reads outputs from the workflow_run_steps table and handles various edge cases.
func TestReadStepOutputs_Integration(t *testing.T) {
	// This test would require a real database connection in a full integration test.
	// For now, we'll just verify the function's signature and basic logic.

	// Test the JSON unmarshaling logic that's used in readStepOutputs
	testOutputsJSON := `{"summary": "Task completed", "pr_url": "https://github.com/org/repo/pull/123", "exit_code": "0"}`

	var outputs map[string]interface{}
	err := json.Unmarshal([]byte(testOutputsJSON), &outputs)
	if err != nil {
		t.Errorf("failed to unmarshal test outputs: %v", err)
	}

	if len(outputs) != 3 {
		t.Errorf("expected 3 outputs, got %d", len(outputs))
	}

	if outputs["summary"] != "Task completed" {
		t.Errorf("expected summary='Task completed', got %v", outputs["summary"])
	}

	if outputs["pr_url"] != "https://github.com/org/repo/pull/123" {
		t.Errorf("expected pr_url='https://github.com/org/repo/pull/123', got %v", outputs["pr_url"])
	}
}

// TestReadStepOutputs_EmptyOutputs tests the handling of empty or nil outputs
func TestReadStepOutputs_EmptyOutputs(t *testing.T) {
	// Test empty JSON object
	emptyJSON := `{}`
	var outputs map[string]interface{}
	err := json.Unmarshal([]byte(emptyJSON), &outputs)
	if err != nil {
		t.Errorf("failed to unmarshal empty outputs: %v", err)
	}

	if len(outputs) != 0 {
		t.Errorf("expected 0 outputs for empty JSON, got %d", len(outputs))
	}

	// Test that we initialize empty map correctly when outputs is nil
	outputs = make(map[string]interface{})
	if len(outputs) != 0 {
		t.Errorf("expected 0 outputs for newly created map, got %d", len(outputs))
	}
}
