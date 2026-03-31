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
	"path/filepath"
	"strings"
)

// EventTrigger defines when a task should be triggered by external events.
type EventTrigger struct {
	GitHub *GitHubTrigger `json:"github,omitempty" yaml:"github"`
}

// GitHubTrigger defines GitHub webhook event matching criteria.
type GitHubTrigger struct {
	Events   []string `json:"events" yaml:"events"`               // push, pull_request, issue_comment, release
	Actions  []string `json:"actions,omitempty" yaml:"actions"`    // opened, synchronize, created, published
	Repos    []string `json:"repos,omitempty" yaml:"repos"`        // org/repo filters (empty = all)
	Branches []string `json:"branches,omitempty" yaml:"branches"`  // branch filters (empty = all)
}

// Matches checks if an incoming webhook event matches this trigger config.
func (t *GitHubTrigger) Matches(eventType, action, repo, branch string) bool {
	if t == nil {
		return false
	}

	// eventType must be in t.Events.
	if !stringInSlice(eventType, t.Events) {
		return false
	}

	// If t.Actions is non-empty, action must be in t.Actions.
	if len(t.Actions) > 0 && !stringInSlice(action, t.Actions) {
		return false
	}

	// If t.Repos is non-empty, repo must match one of the patterns.
	if len(t.Repos) > 0 && !matchesAnyPattern(repo, t.Repos) {
		return false
	}

	// If t.Branches is non-empty, branch must be in t.Branches.
	if len(t.Branches) > 0 && !stringInSlice(branch, t.Branches) {
		return false
	}

	return true
}

// stringInSlice checks if a string is present in a slice (case-insensitive).
func stringInSlice(s string, slice []string) bool {
	for _, v := range slice {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// matchesAnyPattern checks if a string matches any of the given patterns.
// Supports "*" wildcard using filepath.Match semantics, plus exact match.
func matchesAnyPattern(s string, patterns []string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if strings.EqualFold(s, p) {
			return true
		}
		if matched, _ := filepath.Match(p, s); matched {
			return true
		}
	}
	return false
}
