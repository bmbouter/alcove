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
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Evaluator evaluates conditional expressions for workflow step gating.
// Supports both field-based routing (recommended) and complex expressions.
type Evaluator struct{}

// NewEvaluator creates a new condition evaluator.
func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

// EvaluationContext provides the context needed for condition evaluation.
type EvaluationContext struct {
	StepStatuses map[string]string                  // step_id -> status (completed, failed, skipped)
	StepOutputs  map[string]map[string]interface{} // step_id -> output_key -> value
}

// Evaluate evaluates a condition expression against the given context.
// Supports both simple field-based routing and complex multi-step conditions.
func (e *Evaluator) Evaluate(condition string, ctx *EvaluationContext) (bool, error) {
	condition = strings.TrimSpace(condition)

	if condition == "" {
		return true, nil
	}

	// Handle simple boolean values
	if condition == "true" {
		return true, nil
	}
	if condition == "false" {
		return false, nil
	}

	// Try to evaluate as a complex expression first (with boolean operators)
	if strings.Contains(condition, "&&") || strings.Contains(condition, "||") {
		return e.evaluateComplexExpression(condition, ctx)
	}

	// Evaluate as a simple expression
	return e.evaluateSimpleExpression(condition, ctx)
}

// evaluateComplexExpression handles expressions with boolean operators (&&, ||).
func (e *Evaluator) evaluateComplexExpression(condition string, ctx *EvaluationContext) (bool, error) {
	// Split by || first (OR has lower precedence than AND)
	orParts := e.splitByOperator(condition, "||")

	if len(orParts) > 1 {
		// OR logic: at least one part must be true
		for _, part := range orParts {
			result, err := e.evaluateAndExpression(strings.TrimSpace(part), ctx)
			if err != nil {
				return false, err
			}
			if result {
				return true, nil
			}
		}
		return false, nil
	}

	// No OR operator, try AND
	return e.evaluateAndExpression(condition, ctx)
}

// evaluateAndExpression handles AND operations.
func (e *Evaluator) evaluateAndExpression(condition string, ctx *EvaluationContext) (bool, error) {
	andParts := e.splitByOperator(condition, "&&")

	if len(andParts) > 1 {
		// AND logic: all parts must be true
		for _, part := range andParts {
			result, err := e.evaluateSimpleExpression(strings.TrimSpace(part), ctx)
			if err != nil {
				return false, err
			}
			if !result {
				return false, nil
			}
		}
		return true, nil
	}

	// No AND operator, evaluate as simple expression
	return e.evaluateSimpleExpression(condition, ctx)
}

// evaluateSimpleExpression handles single comparison expressions.
func (e *Evaluator) evaluateSimpleExpression(condition string, ctx *EvaluationContext) (bool, error) {
	// Pattern for outcome conditions: steps.stepName.outcome == 'value'
	outcomePattern := regexp.MustCompile(`steps\.(\w+)\.outcome\s*(==|!=)\s*'([^']*)'`)
	if matches := outcomePattern.FindStringSubmatch(condition); len(matches) == 4 {
		stepID := matches[1]
		operator := matches[2]
		expectedValue := matches[3]

		actualValue := ctx.StepStatuses[stepID]
		return e.compareValues(actualValue, operator, expectedValue)
	}

	// Pattern for output conditions: steps.stepName.outputs.outputKey OPERATOR value
	outputPattern := regexp.MustCompile(`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*'([^']*)'`)
	if matches := outputPattern.FindStringSubmatch(condition); len(matches) == 5 {
		stepID := matches[1]
		outputKey := matches[2]
		operator := matches[3]
		expectedValue := matches[4]

		if stepOutput, exists := ctx.StepOutputs[stepID]; exists {
			if actualValue, exists := stepOutput[outputKey]; exists {
				return e.compareValues(actualValue, operator, expectedValue)
			}
		}

		// If step or output doesn't exist, comparison fails
		return false, nil
	}

	// Pattern for numeric output conditions: steps.stepName.outputs.outputKey OPERATOR number
	numericPattern := regexp.MustCompile(`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*(\d+(?:\.\d+)?)`)
	if matches := numericPattern.FindStringSubmatch(condition); len(matches) == 5 {
		stepID := matches[1]
		outputKey := matches[2]
		operator := matches[3]
		expectedValueStr := matches[4]

		expectedValue, err := strconv.ParseFloat(expectedValueStr, 64)
		if err != nil {
			return false, fmt.Errorf("invalid numeric value: %s", expectedValueStr)
		}

		if stepOutput, exists := ctx.StepOutputs[stepID]; exists {
			if actualValue, exists := stepOutput[outputKey]; exists {
				return e.compareValues(actualValue, operator, expectedValue)
			}
		}

		return false, nil
	}

	return false, fmt.Errorf("unrecognized condition format: %s", condition)
}

// compareValues compares two values using the given operator.
func (e *Evaluator) compareValues(actual interface{}, operator string, expected interface{}) (bool, error) {
	switch operator {
	case "==":
		return e.isEqual(actual, expected), nil
	case "!=":
		return !e.isEqual(actual, expected), nil
	case ">", "<", ">=", "<=":
		return e.compareNumerically(actual, operator, expected)
	default:
		return false, fmt.Errorf("unsupported operator: %s", operator)
	}
}

// isEqual checks if two values are equal, handling type conversions.
func (e *Evaluator) isEqual(actual, expected interface{}) bool {
	// Convert both to strings for comparison if they're not already
	actualStr := fmt.Sprintf("%v", actual)
	expectedStr := fmt.Sprintf("%v", expected)
	return actualStr == expectedStr
}

// compareNumerically performs numeric comparison.
func (e *Evaluator) compareNumerically(actual interface{}, operator string, expected interface{}) (bool, error) {
	actualNum, err := e.toFloat64(actual)
	if err != nil {
		return false, fmt.Errorf("cannot convert actual value to number: %v", actual)
	}

	expectedNum, err := e.toFloat64(expected)
	if err != nil {
		return false, fmt.Errorf("cannot convert expected value to number: %v", expected)
	}

	switch operator {
	case ">":
		return actualNum > expectedNum, nil
	case "<":
		return actualNum < expectedNum, nil
	case ">=":
		return actualNum >= expectedNum, nil
	case "<=":
		return actualNum <= expectedNum, nil
	default:
		return false, fmt.Errorf("unsupported numeric operator: %s", operator)
	}
}

// toFloat64 converts various numeric types to float64.
func (e *Evaluator) toFloat64(value interface{}) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case string:
		return strconv.ParseFloat(v, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", value)
	}
}

// splitByOperator splits a string by an operator, respecting quotes.
func (e *Evaluator) splitByOperator(s, operator string) []string {
	var parts []string
	var current strings.Builder
	inQuotes := false

	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			inQuotes = !inQuotes
			current.WriteByte(s[i])
			continue
		}

		if !inQuotes && i+len(operator) <= len(s) && s[i:i+len(operator)] == operator {
			parts = append(parts, current.String())
			current.Reset()
			i += len(operator) - 1 // -1 because loop will increment
			continue
		}

		current.WriteByte(s[i])
	}

	parts = append(parts, current.String())
	return parts
}

// ValidateCondition validates a condition expression syntax without evaluating it.
func (e *Evaluator) ValidateCondition(condition string) error {
	condition = strings.TrimSpace(condition)

	if condition == "" || condition == "true" || condition == "false" {
		return nil
	}

	// For complex expressions, validate each part
	if strings.Contains(condition, "&&") || strings.Contains(condition, "||") {
		return e.validateComplexCondition(condition)
	}

	return e.validateSimpleCondition(condition)
}

// validateComplexCondition validates complex boolean expressions.
func (e *Evaluator) validateComplexCondition(condition string) error {
	// Split by operators and validate each part
	orParts := e.splitByOperator(condition, "||")

	for _, orPart := range orParts {
		andParts := e.splitByOperator(orPart, "&&")
		for _, andPart := range andParts {
			if err := e.validateSimpleCondition(strings.TrimSpace(andPart)); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateSimpleCondition validates a single condition expression.
func (e *Evaluator) validateSimpleCondition(condition string) error {
	// Check for valid patterns
	patterns := []string{
		`steps\.(\w+)\.outcome\s*(==|!=)\s*'([^']*)'`,
		`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*'([^']*)'`,
		`steps\.(\w+)\.outputs\.(\w+)\s*(==|!=|>|<|>=|<=)\s*(\d+(?:\.\d+)?)`,
	}

	for _, pattern := range patterns {
		matched, err := regexp.MatchString(pattern, condition)
		if err != nil {
			return fmt.Errorf("error matching pattern: %w", err)
		}
		if matched {
			return nil
		}
	}

	return fmt.Errorf("invalid condition syntax: %s", condition)
}