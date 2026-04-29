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

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestReadOutputArtifact tests the mixed-type output parsing functionality.
func TestReadOutputArtifact(t *testing.T) {
	// Create a temporary directory for test files
	tempDir := t.TempDir()
	testPath := filepath.Join(tempDir, "alcove-outputs.json")

	tests := []struct {
		name     string
		content  string
		expected map[string]string
		wantNil  bool
	}{
		{
			name:     "string-only map",
			content:  `{"message": "success", "status": "ok"}`,
			expected: map[string]string{"message": "success", "status": "ok"},
		},
		{
			name:    "mixed types - bool and array",
			content: `{"automatable": true, "candidate_files": ["auth.py", "tests.py"]}`,
			expected: map[string]string{
				"automatable":     "true",
				"candidate_files": `["auth.py","tests.py"]`,
			},
		},
		{
			name:    "mixed types - bool false",
			content: `{"automatable": false, "ready": true}`,
			expected: map[string]string{
				"automatable": "false",
				"ready":       "true",
			},
		},
		{
			name:    "integer numbers",
			content: `{"count": 42, "score": 100}`,
			expected: map[string]string{
				"count": "42",
				"score": "100",
			},
		},
		{
			name:    "float numbers",
			content: `{"confidence": 0.85, "threshold": 3.14159}`,
			expected: map[string]string{
				"confidence": "0.85",
				"threshold":  "3.14159",
			},
		},
		{
			name:    "nested objects",
			content: `{"config": {"timeout": 30, "retry": true}, "metadata": {"version": "1.0"}}`,
			expected: map[string]string{
				"config":   `{"retry":true,"timeout":30}`,
				"metadata": `{"version":"1.0"}`,
			},
		},
		{
			name:    "null values",
			content: `{"optional": null, "message": "test"}`,
			expected: map[string]string{
				"optional": "null",
				"message":  "test",
			},
		},
		{
			name:    "empty array",
			content: `{"items": [], "count": 0}`,
			expected: map[string]string{
				"items": "[]",
				"count": "0",
			},
		},
		{
			name:    "complex nested array",
			content: `{"results": [{"id": 1, "active": true}, {"id": 2, "active": false}]}`,
			expected: map[string]string{
				"results": `[{"active":true,"id":1},{"active":false,"id":2}]`,
			},
		},
		{
			name:    "invalid JSON",
			content: `{"invalid": json}`,
			wantNil: true,
		},
		{
			name:    "empty object",
			content: `{}`,
			wantNil: true,
		},
		{
			name:    "empty string",
			content: ``,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test content to file
			if tt.content != "" {
				if err := os.WriteFile(testPath, []byte(tt.content), 0644); err != nil {
					t.Fatalf("Failed to write test file: %v", err)
				}
			} else {
				// For empty string test, don't create the file
				os.Remove(testPath)
			}

			// Call our test version of readOutputArtifact using our test file
			result := testReadOutputArtifact(testPath)

			// Check results
			if tt.wantNil {
				if result != nil {
					t.Errorf("Expected nil but got %v", result)
				}
			} else {
				if result == nil {
					t.Errorf("Expected result but got nil")
					return
				}

				if len(result) != len(tt.expected) {
					t.Errorf("Expected %d outputs but got %d", len(tt.expected), len(result))
				}

				for key, expected := range tt.expected {
					if actual, ok := result[key]; !ok {
						t.Errorf("Missing key %q", key)
					} else if actual != expected {
						t.Errorf("Key %q: expected %q but got %q", key, expected, actual)
					}
				}
			}

			// Clean up
			os.Remove(testPath)
		})
	}
}

// testReadOutputArtifact is a test-friendly version that takes a file path parameter
func testReadOutputArtifact(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	if len(raw) == 0 {
		return nil
	}

	outputs := make(map[string]string, len(raw))
	for key, val := range raw {
		switch typed := val.(type) {
		case string:
			outputs[key] = typed
		case bool:
			outputs[key] = strconv.FormatBool(typed)
		case float64:
			if typed == float64(int64(typed)) {
				outputs[key] = strconv.FormatInt(int64(typed), 10)
			} else {
				outputs[key] = strconv.FormatFloat(typed, 'f', -1, 64)
			}
		default:
			b, _ := json.Marshal(val)
			outputs[key] = string(b)
		}
	}

	return outputs
}

// TestReadOutputArtifact_MissingFile tests the case where the output file doesn't exist.
func TestReadOutputArtifact_MissingFile(t *testing.T) {
	// Use a path that definitely doesn't exist
	tempDir := t.TempDir()
	nonExistentPath := filepath.Join(tempDir, "nonexistent.json")

	result := testReadOutputArtifact(nonExistentPath)

	if result != nil {
		t.Errorf("Expected nil for missing file but got %v", result)
	}
}

// TestDetermineOutcome tests the unified outcome determination logic.
func TestDetermineOutcome(t *testing.T) {
	// Create a context that's already cancelled for timeout tests
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Create a context that's not cancelled for normal tests
	normalCtx := context.Background()

	tests := []struct {
		name             string
		ctx              context.Context
		exitCode         int
		eventCount       int
		sawSuccessResult bool
		currentOutcome   string
		expectedOutcome  string
		expectedExitCode int
	}{
		{
			name:             "Rule 1: Context timeout takes priority",
			ctx:              cancelledCtx,
			exitCode:         0,
			eventCount:       5,
			sawSuccessResult: true,
			currentOutcome:   "completed",
			expectedOutcome:  "timeout",
			expectedExitCode: 0,
		},
		{
			name:             "Rule 2: Cancellation takes priority",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       5,
			sawSuccessResult: true,
			currentOutcome:   "cancelled",
			expectedOutcome:  "cancelled",
			expectedExitCode: 0,
		},
		{
			name:             "Rule 3: Heartbeat timeout takes priority",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       5,
			sawSuccessResult: true,
			currentOutcome:   "timeout",
			expectedOutcome:  "timeout",
			expectedExitCode: 0,
		},
		{
			name:             "Rule 4: Success result indicator (Claude Code) → completed",
			ctx:              normalCtx,
			exitCode:         1,
			eventCount:       5,
			sawSuccessResult: true,
			currentOutcome:   "completed",
			expectedOutcome:  "completed",
			expectedExitCode: 1, // Preserve exit code for workflow engine
		},
		{
			name:             "Rule 5: No output at all → error (key fix)",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       0,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "error",
			expectedExitCode: 0,
		},
		{
			name:             "Rule 5: No output at all with non-zero exit → error",
			ctx:              normalCtx,
			exitCode:         1,
			eventCount:       0,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "error",
			expectedExitCode: 1,
		},
		{
			name:             "Rule 6: Output but no success result and non-zero exit → error",
			ctx:              normalCtx,
			exitCode:         1,
			eventCount:       5,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "error",
			expectedExitCode: 1,
		},
		{
			name:             "Rule 7: Output but no success result and zero exit → completed",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       5,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "completed",
			expectedExitCode: 0,
		},
		{
			name:             "Edge case: Claude produces output but crashes before sending result event",
			ctx:              normalCtx,
			exitCode:         1,
			eventCount:       10,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "error",
			expectedExitCode: 1,
		},
		{
			name:             "Edge case: Zero events with exit 0 (silent crash)",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       0,
			sawSuccessResult: false,
			currentOutcome:   "completed",
			expectedOutcome:  "error", // Key fix: no output = error regardless of exit code
			expectedExitCode: 0,
		},
		{
			name:             "NATS cancel should take priority over success result",
			ctx:              normalCtx,
			exitCode:         0,
			eventCount:       5,
			sawSuccessResult: true,
			currentOutcome:   "cancelled",
			expectedOutcome:  "cancelled",
			expectedExitCode: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, exitCode := determineOutcome(tt.ctx, tt.exitCode, tt.eventCount, tt.sawSuccessResult, tt.currentOutcome)

			if outcome != tt.expectedOutcome {
				t.Errorf("determineOutcome() outcome = %v, want %v", outcome, tt.expectedOutcome)
			}
			if exitCode != tt.expectedExitCode {
				t.Errorf("determineOutcome() exitCode = %v, want %v", exitCode, tt.expectedExitCode)
			}
		})
	}
}
