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
	"fmt"
	"strings"
	"unicode"
)

// EvaluateDepends evaluates a dependency expression against current step statuses.
//
// Expressions support:
//   - "step.Succeeded" — true if step status is "completed"
//   - "step.Failed" — true if step status is "failed"
//   - "&&" — logical AND
//   - "||" — logical OR
//   - Parentheses for grouping
//
// An unresolved step (not in the map or status is "pending"/"running") evaluates to false.
func EvaluateDepends(expr string, stepStatuses map[string]string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}

	tokens, err := tokenizeDepends(expr)
	if err != nil {
		return false, err
	}

	p := &dependsParser{tokens: tokens, stepStatuses: stepStatuses}
	result, err := p.parseOrExpr()
	if err != nil {
		return false, err
	}

	if p.pos < len(p.tokens) {
		return false, fmt.Errorf("unexpected token at position %d: %q", p.pos, p.tokens[p.pos])
	}

	return result, nil
}

// ExtractDependsStepIDs extracts all step IDs referenced in a depends expression.
func ExtractDependsStepIDs(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}

	tokens, err := tokenizeDepends(expr)
	if err != nil {
		return nil
	}

	var stepIDs []string
	seen := make(map[string]bool)
	for _, tok := range tokens {
		if strings.Contains(tok, ".") {
			parts := strings.SplitN(tok, ".", 2)
			stepID := parts[0]
			if !seen[stepID] {
				seen[stepID] = true
				stepIDs = append(stepIDs, stepID)
			}
		}
	}
	return stepIDs
}

// NeedsToDepends converts a legacy Needs list (all must be completed) to a Depends expression.
func NeedsToDepends(needs []string) string {
	if len(needs) == 0 {
		return ""
	}
	parts := make([]string, len(needs))
	for i, n := range needs {
		parts[i] = n + ".Succeeded"
	}
	return strings.Join(parts, " && ")
}

// tokenizeDepends splits a depends expression into tokens.
// Tokens: identifiers like "step.Succeeded", operators "&&" and "||", and parentheses.
func tokenizeDepends(expr string) ([]string, error) {
	var tokens []string
	i := 0
	runes := []rune(expr)

	for i < len(runes) {
		ch := runes[i]

		// Skip whitespace
		if unicode.IsSpace(ch) {
			i++
			continue
		}

		// Parentheses
		if ch == '(' || ch == ')' {
			tokens = append(tokens, string(ch))
			i++
			continue
		}

		// Operators: && and ||
		if ch == '&' && i+1 < len(runes) && runes[i+1] == '&' {
			tokens = append(tokens, "&&")
			i += 2
			continue
		}
		if ch == '|' && i+1 < len(runes) && runes[i+1] == '|' {
			tokens = append(tokens, "||")
			i += 2
			continue
		}

		// Identifier: letters, digits, underscores, hyphens, and dots
		if isIdentStart(ch) {
			start := i
			for i < len(runes) && isIdentChar(runes[i]) {
				i++
			}
			tokens = append(tokens, string(runes[start:i]))
			continue
		}

		return nil, fmt.Errorf("unexpected character %q at position %d", string(ch), i)
	}

	return tokens, nil
}

func isIdentStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

func isIdentChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '-' || ch == '.'
}

// dependsParser is a recursive-descent parser for depends expressions.
type dependsParser struct {
	tokens       []string
	pos          int
	stepStatuses map[string]string
}

// parseOrExpr: expr = andExpr ( "||" andExpr )*
func (p *dependsParser) parseOrExpr() (bool, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return false, err
	}

	for p.pos < len(p.tokens) && p.tokens[p.pos] == "||" {
		p.pos++ // consume "||"
		right, err := p.parseAndExpr()
		if err != nil {
			return false, err
		}
		left = left || right
	}

	return left, nil
}

// parseAndExpr: andExpr = primary ( "&&" primary )*
func (p *dependsParser) parseAndExpr() (bool, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return false, err
	}

	for p.pos < len(p.tokens) && p.tokens[p.pos] == "&&" {
		p.pos++ // consume "&&"
		right, err := p.parsePrimary()
		if err != nil {
			return false, err
		}
		left = left && right
	}

	return left, nil
}

// parsePrimary: primary = "(" orExpr ")" | stepRef
func (p *dependsParser) parsePrimary() (bool, error) {
	if p.pos >= len(p.tokens) {
		return false, fmt.Errorf("unexpected end of expression")
	}

	tok := p.tokens[p.pos]

	if tok == "(" {
		p.pos++ // consume "("
		result, err := p.parseOrExpr()
		if err != nil {
			return false, err
		}
		if p.pos >= len(p.tokens) || p.tokens[p.pos] != ")" {
			return false, fmt.Errorf("expected ')' but got end of expression")
		}
		p.pos++ // consume ")"
		return result, nil
	}

	// Must be a step reference like "stepID.Succeeded" or "stepID.Failed"
	return p.parseStepRef(tok)
}

// parseStepRef evaluates a "stepID.Status" reference.
func (p *dependsParser) parseStepRef(tok string) (bool, error) {
	parts := strings.SplitN(tok, ".", 2)
	if len(parts) != 2 {
		return false, fmt.Errorf("invalid step reference %q: expected format 'step.Succeeded' or 'step.Failed'", tok)
	}

	stepID := parts[0]
	statusCheck := parts[1]

	p.pos++ // consume the token

	currentStatus, exists := p.stepStatuses[stepID]
	if !exists {
		return false, nil // unresolved step -> false
	}

	switch statusCheck {
	case "Succeeded":
		return currentStatus == "completed", nil
	case "Failed":
		return currentStatus == "failed", nil
	default:
		return false, fmt.Errorf("unknown status check %q in step reference %q: expected 'Succeeded' or 'Failed'", statusCheck, tok)
	}
}
