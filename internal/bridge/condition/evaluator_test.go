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

package condition

import (
	"testing"
)

func TestEvaluator_SimpleConditions(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name      string
		condition string
		context   *EvaluationContext
		expected  bool
		hasError  bool
	}{
		{
			name:      "empty condition defaults to true",
			condition: "",
			context:   &EvaluationContext{},
			expected:  true,
			hasError:  false,
		},
		{
			name:      "true literal",
			condition: "true",
			context:   &EvaluationContext{},
			expected:  true,
			hasError:  false,
		},
		{
			name:      "false literal",
			condition: "false",
			context:   &EvaluationContext{},
			expected:  false,
			hasError:  false,
		},
		{
			name:      "step outcome equals completed",
			condition: "steps.implement.outcome == 'completed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "completed",
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "step outcome not equal to completed",
			condition: "steps.implement.outcome != 'failed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "completed",
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "step outcome comparison fails",
			condition: "steps.implement.outcome == 'completed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "failed",
				},
			},
			expected: false,
			hasError: false,
		},
		{
			name:      "missing step defaults to empty string",
			condition: "steps.missing.outcome == 'completed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{},
			},
			expected: false,
			hasError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(test.condition, test.context)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestEvaluator_OutputConditions(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name      string
		condition string
		context   *EvaluationContext
		expected  bool
		hasError  bool
	}{
		{
			name:      "string output equals",
			condition: "steps.test.outputs.status == 'success'",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "success"},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "string output not equals",
			condition: "steps.test.outputs.status != 'failed'",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "success"},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "numeric output greater than",
			condition: "steps.test.outputs.coverage > 80",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 85.5},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "numeric output less than",
			condition: "steps.test.outputs.coverage < 90",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 85.5},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "numeric output greater than or equal",
			condition: "steps.test.outputs.coverage >= 85",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 85.0},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "numeric output less than or equal",
			condition: "steps.test.outputs.coverage <= 85",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 84.9},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "missing step output",
			condition: "steps.missing.outputs.value == 'test'",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{},
			},
			expected: false,
			hasError: false,
		},
		{
			name:      "missing output key",
			condition: "steps.test.outputs.missing == 'test'",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"other": "value"},
				},
			},
			expected: false,
			hasError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(test.condition, test.context)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestEvaluator_ComplexConditions(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name      string
		condition string
		context   *EvaluationContext
		expected  bool
		hasError  bool
	}{
		{
			name:      "AND condition both true",
			condition: "steps.implement.outcome == 'completed' && steps.test.outputs.status == 'success'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "completed",
				},
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "success"},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "AND condition first false",
			condition: "steps.implement.outcome == 'completed' && steps.test.outputs.status == 'success'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "failed",
				},
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "success"},
				},
			},
			expected: false,
			hasError: false,
		},
		{
			name:      "OR condition first true",
			condition: "steps.implement.outcome == 'completed' || steps.test.outputs.status == 'success'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "completed",
				},
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "failed"},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "OR condition both false",
			condition: "steps.implement.outcome == 'completed' || steps.test.outputs.status == 'success'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"implement": "failed",
				},
				StepOutputs: map[string]map[string]interface{}{
					"test": {"status": "failed"},
				},
			},
			expected: false,
			hasError: false,
		},
		{
			name:      "mixed AND/OR precedence",
			condition: "steps.a.outcome == 'completed' && steps.b.outcome == 'completed' || steps.c.outcome == 'completed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"a": "failed",
					"b": "completed",
					"c": "completed",
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "numeric comparison in complex condition",
			condition: "steps.test.outputs.coverage > 80 && steps.test.outcome == 'completed'",
			context: &EvaluationContext{
				StepStatuses: map[string]string{
					"test": "completed",
				},
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 85},
				},
			},
			expected: true,
			hasError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(test.condition, test.context)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestEvaluator_NumericComparison(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name      string
		condition string
		context   *EvaluationContext
		expected  bool
		hasError  bool
	}{
		{
			name:      "int value greater than",
			condition: "steps.test.outputs.score > 85",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"score": 90},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "float value comparison",
			condition: "steps.test.outputs.coverage >= 85.5",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"coverage": 85.5},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "string number comparison",
			condition: "steps.test.outputs.count < 100",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"count": "95"},
				},
			},
			expected: true,
			hasError: false,
		},
		{
			name:      "invalid number comparison",
			condition: "steps.test.outputs.text > 50",
			context: &EvaluationContext{
				StepOutputs: map[string]map[string]interface{}{
					"test": {"text": "not_a_number"},
				},
			},
			expected: false,
			hasError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(test.condition, test.context)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !test.hasError && result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}

func TestEvaluator_SplitByOperator(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name     string
		input    string
		operator string
		expected []string
	}{
		{
			name:     "simple AND split",
			input:    "a && b",
			operator: "&&",
			expected: []string{"a ", " b"},
		},
		{
			name:     "multiple AND splits",
			input:    "a && b && c",
			operator: "&&",
			expected: []string{"a ", " b ", " c"},
		},
		{
			name:     "respect quotes",
			input:    "steps.test.outputs.message == 'hello && world' && steps.other.outcome == 'completed'",
			operator: "&&",
			expected: []string{"steps.test.outputs.message == 'hello && world' ", " steps.other.outcome == 'completed'"},
		},
		{
			name:     "no operator",
			input:    "simple condition",
			operator: "&&",
			expected: []string{"simple condition"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := evaluator.splitByOperator(test.input, test.operator)
			if len(result) != len(test.expected) {
				t.Errorf("expected %d parts, got %d", len(test.expected), len(result))
				return
			}
			for i, expected := range test.expected {
				if result[i] != expected {
					t.Errorf("part %d: expected '%s', got '%s'", i, expected, result[i])
				}
			}
		})
	}
}

func TestEvaluator_ValidateCondition(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name      string
		condition string
		hasError  bool
	}{
		{
			name:      "empty condition",
			condition: "",
			hasError:  false,
		},
		{
			name:      "boolean literals",
			condition: "true",
			hasError:  false,
		},
		{
			name:      "valid outcome condition",
			condition: "steps.implement.outcome == 'completed'",
			hasError:  false,
		},
		{
			name:      "valid output condition",
			condition: "steps.test.outputs.status == 'success'",
			hasError:  false,
		},
		{
			name:      "valid numeric condition",
			condition: "steps.test.outputs.coverage >= 85",
			hasError:  false,
		},
		{
			name:      "valid complex condition",
			condition: "steps.a.outcome == 'completed' && steps.b.outputs.status == 'success'",
			hasError:  false,
		},
		{
			name:      "invalid syntax",
			condition: "invalid.syntax.here",
			hasError:  true,
		},
		{
			name:      "missing quotes",
			condition: "steps.test.outcome == completed",
			hasError:  true,
		},
		{
			name:      "invalid operator",
			condition: "steps.test.outcome === 'completed'",
			hasError:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := evaluator.ValidateCondition(test.condition)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestEvaluator_TypeConversion(t *testing.T) {
	evaluator := NewEvaluator()

	tests := []struct {
		name     string
		value    interface{}
		expected float64
		hasError bool
	}{
		{name: "float64", value: 85.5, expected: 85.5, hasError: false},
		{name: "float32", value: float32(85.5), expected: 85.5, hasError: false},
		{name: "int", value: 85, expected: 85.0, hasError: false},
		{name: "int32", value: int32(85), expected: 85.0, hasError: false},
		{name: "int64", value: int64(85), expected: 85.0, hasError: false},
		{name: "string number", value: "85.5", expected: 85.5, hasError: false},
		{name: "string non-number", value: "not_a_number", expected: 0, hasError: true},
		{name: "boolean", value: true, expected: 0, hasError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.toFloat64(test.value)

			if test.hasError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !test.hasError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !test.hasError && result != test.expected {
				t.Errorf("expected %v, got %v", test.expected, result)
			}
		})
	}
}