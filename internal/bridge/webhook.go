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
	Jira   *JiraTrigger   `json:"jira,omitempty" yaml:"jira"`
}

// JiraTrigger defines JIRA issue matching criteria for workflow triggers.
type JiraTrigger struct {
	Projects   []string `json:"projects" yaml:"projects"`                       // JIRA project keys (e.g., "RHCLOUD", "AAP")
	Components []string `json:"components,omitempty" yaml:"components,omitempty"` // component filters (empty = all)
	Labels     []string `json:"labels,omitempty" yaml:"labels,omitempty"`         // label filters (empty = all)
}

// Matches checks if a JIRA issue matches this trigger config.
func (t *JiraTrigger) Matches(issueProject string, issueComponents, issueLabels []string) bool {
	if t == nil {
		return false
	}

	// Issue project must be in t.Projects.
	if !stringInSlice(issueProject, t.Projects) {
		return false
	}

	// If t.Components is non-empty, at least one must match.
	if len(t.Components) > 0 {
		matched := false
		for _, required := range t.Components {
			for _, have := range issueComponents {
				if strings.EqualFold(required, have) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	// If t.Labels is non-empty, at least one must match.
	if len(t.Labels) > 0 {
		matched := false
		for _, required := range t.Labels {
			for _, have := range issueLabels {
				if strings.EqualFold(required, have) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// GitHubTrigger defines GitHub webhook event matching criteria.
type GitHubTrigger struct {
	Events       []string `json:"events" yaml:"events"`                              // push, pull_request, issue_comment, release
	Actions      []string `json:"actions,omitempty" yaml:"actions"`                   // opened, synchronize, created, published
	Repos        []string `json:"repos,omitempty" yaml:"repos"`                       // org/repo filters (empty = all)
	Branches     []string `json:"branches,omitempty" yaml:"branches"`                 // branch filters (empty = all)
	Labels       []string `json:"labels,omitempty" yaml:"labels"`                     // label filters (empty = all)
	Users        []string `json:"users,omitempty" yaml:"users"`                       // user filters (empty = all)
	DeliveryMode string   `json:"delivery_mode,omitempty" yaml:"delivery_mode"`       // "polling" or "webhook", default "polling"
}

// Matches checks if an incoming webhook event matches this trigger config.
func (t *GitHubTrigger) Matches(eventType, action, repo, branch string, labels, users []string) bool {
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

	// Labels filter (AND with other filters). If trigger specifies labels,
	// at least one must be present on the issue/PR.
	if len(t.Labels) > 0 {
		matched := false
		for _, required := range t.Labels {
			for _, have := range labels {
				if strings.EqualFold(required, have) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Users filter (AND with other filters). If trigger specifies users,
	// at least one must match the event's user.
	if len(t.Users) > 0 {
		matched := false
		for _, required := range t.Users {
			for _, have := range users {
				if strings.EqualFold(required, have) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
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
