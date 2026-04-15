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

func TestEvaluateDepends_Simple(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		statuses map[string]string
		expected bool
		hasError bool
	}{
		{
			name:     "A.Succeeded with A completed",
			expr:     "A.Succeeded",
			statuses: map[string]string{"A": "completed"},
			expected: true,
		},
		{
			name:     "A.Succeeded with A failed",
			expr:     "A.Succeeded",
			statuses: map[string]string{"A": "failed"},
			expected: false,
		},
		{
			name:     "A.Succeeded with A pending",
			expr:     "A.Succeeded",
			statuses: map[string]string{"A": "pending"},
			expected: false,
		},
		{
			name:     "A.Succeeded with A running",
			expr:     "A.Succeeded",
			statuses: map[string]string{"A": "running"},
			expected: false,
		},
		{
			name:     "A.Succeeded with A not in map",
			expr:     "A.Succeeded",
			statuses: map[string]string{},
			expected: false,
		},
		{
			name:     "A.Failed with A failed",
			expr:     "A.Failed",
			statuses: map[string]string{"A": "failed"},
			expected: true,
		},
		{
			name:     "A.Failed with A completed",
			expr:     "A.Failed",
			statuses: map[string]string{"A": "completed"},
			expected: false,
		},
		{
			name:     "empty expression",
			expr:     "",
			statuses: map[string]string{},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateDepends(tt.expr, tt.statuses)
			if tt.hasError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEvaluateDepends_AND(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		statuses map[string]string
		expected bool
	}{
		{
			name:     "both completed",
			expr:     "A.Succeeded && B.Succeeded",
			statuses: map[string]string{"A": "completed", "B": "completed"},
			expected: true,
		},
		{
			name:     "one failed",
			expr:     "A.Succeeded && B.Succeeded",
			statuses: map[string]string{"A": "completed", "B": "failed"},
			expected: false,
		},
		{
			name:     "one pending",
			expr:     "A.Succeeded && B.Succeeded",
			statuses: map[string]string{"A": "completed", "B": "pending"},
			expected: false,
		},
		{
			name:     "both failed",
			expr:     "A.Succeeded && B.Succeeded",
			statuses: map[string]string{"A": "failed", "B": "failed"},
			expected: false,
		},
		{
			name:     "three way AND all completed",
			expr:     "A.Succeeded && B.Succeeded && C.Succeeded",
			statuses: map[string]string{"A": "completed", "B": "completed", "C": "completed"},
			expected: true,
		},
		{
			name:     "three way AND one missing",
			expr:     "A.Succeeded && B.Succeeded && C.Succeeded",
			statuses: map[string]string{"A": "completed", "B": "completed"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateDepends(tt.expr, tt.statuses)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEvaluateDepends_OR(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		statuses map[string]string
		expected bool
	}{
		{
			name:     "one failed",
			expr:     "A.Failed || B.Failed",
			statuses: map[string]string{"A": "failed", "B": "completed"},
			expected: true,
		},
		{
			name:     "neither failed",
			expr:     "A.Failed || B.Failed",
			statuses: map[string]string{"A": "completed", "B": "completed"},
			expected: false,
		},
		{
			name:     "both failed",
			expr:     "A.Failed || B.Failed",
			statuses: map[string]string{"A": "failed", "B": "failed"},
			expected: true,
		},
		{
			name:     "one pending one failed",
			expr:     "A.Failed || B.Failed",
			statuses: map[string]string{"A": "pending", "B": "failed"},
			expected: true,
		},
		{
			name:     "succeeded or succeeded - one done",
			expr:     "await-ci.Succeeded || revision.Succeeded",
			statuses: map[string]string{"await-ci": "completed", "revision": "pending"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateDepends(tt.expr, tt.statuses)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEvaluateDepends_Mixed(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		statuses map[string]string
		expected bool
	}{
		{
			name:     "A succeeded and B failed",
			expr:     "A.Succeeded && (B.Failed || C.Succeeded)",
			statuses: map[string]string{"A": "completed", "B": "failed", "C": "pending"},
			expected: true,
		},
		{
			name:     "A succeeded and C succeeded",
			expr:     "A.Succeeded && (B.Failed || C.Succeeded)",
			statuses: map[string]string{"A": "completed", "B": "completed", "C": "completed"},
			expected: true,
		},
		{
			name:     "A succeeded but neither B failed nor C succeeded",
			expr:     "A.Succeeded && (B.Failed || C.Succeeded)",
			statuses: map[string]string{"A": "completed", "B": "completed", "C": "pending"},
			expected: false,
		},
		{
			name:     "A not succeeded",
			expr:     "A.Succeeded && (B.Failed || C.Succeeded)",
			statuses: map[string]string{"A": "pending", "B": "failed", "C": "completed"},
			expected: false,
		},
		{
			name:     "code-review and security-review both succeeded",
			expr:     "code-review.Succeeded && security-review.Succeeded",
			statuses: map[string]string{"code-review": "completed", "security-review": "completed"},
			expected: true,
		},
		{
			name:     "nested parentheses",
			expr:     "(A.Succeeded && B.Succeeded) || (C.Failed && D.Succeeded)",
			statuses: map[string]string{"A": "failed", "B": "completed", "C": "failed", "D": "completed"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateDepends(tt.expr, tt.statuses)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestEvaluateDepends_Invalid(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{
			name: "missing status",
			expr: "A",
		},
		{
			name: "unknown status check",
			expr: "A.Running",
		},
		{
			name: "unmatched paren",
			expr: "(A.Succeeded && B.Succeeded",
		},
		{
			name: "invalid character",
			expr: "A.Succeeded & B.Succeeded",
		},
		{
			name: "empty parens",
			expr: "()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EvaluateDepends(tt.expr, map[string]string{"A": "completed", "B": "completed"})
			if err == nil {
				t.Error("expected error but got none")
			}
		})
	}
}

func TestExtractDependsStepIDs(t *testing.T) {
	tests := []struct {
		name     string
		expr     string
		expected []string
	}{
		{
			name:     "single step",
			expr:     "A.Succeeded",
			expected: []string{"A"},
		},
		{
			name:     "multiple steps",
			expr:     "A.Succeeded && B.Failed",
			expected: []string{"A", "B"},
		},
		{
			name:     "duplicate step",
			expr:     "A.Succeeded || A.Failed",
			expected: []string{"A"},
		},
		{
			name:     "complex expression",
			expr:     "A.Succeeded && (B.Failed || C.Succeeded)",
			expected: []string{"A", "B", "C"},
		},
		{
			name:     "empty expression",
			expr:     "",
			expected: nil,
		},
		{
			name:     "hyphenated step IDs",
			expr:     "code-review.Succeeded && await-ci.Succeeded",
			expected: []string{"code-review", "await-ci"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractDependsStepIDs(tt.expr)
			if len(result) != len(tt.expected) {
				t.Fatalf("expected %d step IDs, got %d: %v", len(tt.expected), len(result), result)
			}
			for i, id := range tt.expected {
				if result[i] != id {
					t.Errorf("expected step ID %q at index %d, got %q", id, i, result[i])
				}
			}
		})
	}
}

func TestNeedsToDepends(t *testing.T) {
	tests := []struct {
		name     string
		needs    []string
		expected string
	}{
		{
			name:     "empty",
			needs:    nil,
			expected: "",
		},
		{
			name:     "single",
			needs:    []string{"step1"},
			expected: "step1.Succeeded",
		},
		{
			name:     "multiple",
			needs:    []string{"step1", "step2"},
			expected: "step1.Succeeded && step2.Succeeded",
		},
		{
			name:     "three items",
			needs:    []string{"a", "b", "c"},
			expected: "a.Succeeded && b.Succeeded && c.Succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NeedsToDepends(tt.needs)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
