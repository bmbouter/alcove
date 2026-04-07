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

import "testing"

func TestGitHubTriggerMatches(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *GitHubTrigger
		eventType string
		action    string
		repo      string
		branch    string
		labels    []string
		want      bool
	}{
		{
			name:      "nil trigger",
			trigger:   nil,
			eventType: "push",
			want:      false,
		},
		{
			name:      "matching push event",
			trigger:   &GitHubTrigger{Events: []string{"push"}},
			eventType: "push",
			want:      true,
		},
		{
			name:      "non-matching event type",
			trigger:   &GitHubTrigger{Events: []string{"push"}},
			eventType: "pull_request",
			want:      false,
		},
		{
			name:      "matching event with action filter",
			trigger:   &GitHubTrigger{Events: []string{"pull_request"}, Actions: []string{"opened", "synchronize"}},
			eventType: "pull_request",
			action:    "opened",
			want:      true,
		},
		{
			name:      "non-matching action",
			trigger:   &GitHubTrigger{Events: []string{"pull_request"}, Actions: []string{"opened"}},
			eventType: "pull_request",
			action:    "closed",
			want:      false,
		},
		{
			name:      "matching repo filter",
			trigger:   &GitHubTrigger{Events: []string{"push"}, Repos: []string{"org/repo"}},
			eventType: "push",
			repo:      "org/repo",
			want:      true,
		},
		{
			name:      "non-matching repo filter",
			trigger:   &GitHubTrigger{Events: []string{"push"}, Repos: []string{"org/repo"}},
			eventType: "push",
			repo:      "other/repo",
			want:      false,
		},
		{
			name:      "wildcard repo filter",
			trigger:   &GitHubTrigger{Events: []string{"push"}, Repos: []string{"*"}},
			eventType: "push",
			repo:      "any/repo",
			want:      true,
		},
		{
			name:      "matching branch filter",
			trigger:   &GitHubTrigger{Events: []string{"push"}, Branches: []string{"main", "develop"}},
			eventType: "push",
			branch:    "main",
			want:      true,
		},
		{
			name:      "non-matching branch filter",
			trigger:   &GitHubTrigger{Events: []string{"push"}, Branches: []string{"main"}},
			eventType: "push",
			branch:    "feature-x",
			want:      false,
		},
		{
			name:      "empty filters match all",
			trigger:   &GitHubTrigger{Events: []string{"push"}},
			eventType: "push",
			action:    "any",
			repo:      "any/repo",
			branch:    "any-branch",
			want:      true,
		},
		{
			name: "all filters combined - match",
			trigger: &GitHubTrigger{
				Events:   []string{"pull_request"},
				Actions:  []string{"opened"},
				Repos:    []string{"org/repo"},
				Branches: []string{"main"},
			},
			eventType: "pull_request",
			action:    "opened",
			repo:      "org/repo",
			branch:    "main",
			want:      true,
		},
		{
			name: "all filters combined - repo mismatch",
			trigger: &GitHubTrigger{
				Events:   []string{"pull_request"},
				Actions:  []string{"opened"},
				Repos:    []string{"org/repo"},
				Branches: []string{"main"},
			},
			eventType: "pull_request",
			action:    "opened",
			repo:      "other/repo",
			branch:    "main",
			want:      false,
		},
		{
			name:      "case insensitive event matching",
			trigger:   &GitHubTrigger{Events: []string{"Push"}},
			eventType: "push",
			want:      true,
		},
		// Label filter tests
		{
			name:      "trigger with labels + event with matching label",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"ready-for-dev"}},
			eventType: "issues",
			labels:    []string{"bug", "ready-for-dev"},
			want:      true,
		},
		{
			name:      "trigger with labels + event with no labels",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"ready-for-dev"}},
			eventType: "issues",
			labels:    nil,
			want:      false,
		},
		{
			name:      "trigger with labels + event with different labels",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"ready-for-dev"}},
			eventType: "issues",
			labels:    []string{"bug", "enhancement"},
			want:      false,
		},
		{
			name:      "trigger with no labels + event with any labels",
			trigger:   &GitHubTrigger{Events: []string{"issues"}},
			eventType: "issues",
			labels:    []string{"bug", "enhancement"},
			want:      true,
		},
		{
			name:      "case insensitive label matching",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"Ready-For-Dev"}},
			eventType: "issues",
			labels:    []string{"ready-for-dev"},
			want:      true,
		},
		{
			name:      "trigger with multiple labels + event matches one",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"ready-for-dev", "approved"}},
			eventType: "issues",
			labels:    []string{"approved"},
			want:      true,
		},
		{
			name:      "trigger with labels + empty labels slice",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Labels: []string{"ready-for-dev"}},
			eventType: "issues",
			labels:    []string{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.trigger.Matches(tt.eventType, tt.action, tt.repo, tt.branch, tt.labels)
			if got != tt.want {
				t.Errorf("Matches(%q, %q, %q, %q, %v) = %v, want %v",
					tt.eventType, tt.action, tt.repo, tt.branch, tt.labels, got, tt.want)
			}
		})
	}
}
