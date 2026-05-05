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
		users     []string
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
		// User filter tests
		{
			name:      "trigger with users + matching user",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Users: []string{"bmbouter"}},
			eventType: "issues",
			users:     []string{"bmbouter"},
			want:      true,
		},
		{
			name:      "trigger with users + no users",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Users: []string{"bmbouter"}},
			eventType: "issues",
			users:     nil,
			want:      false,
		},
		{
			name:      "trigger with users + different user",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Users: []string{"bmbouter"}},
			eventType: "issues",
			users:     []string{"otheruser"},
			want:      false,
		},
		{
			name:      "trigger with no users filter + any event",
			trigger:   &GitHubTrigger{Events: []string{"issues"}},
			eventType: "issues",
			users:     []string{"anyuser"},
			want:      true,
		},
		{
			name:      "case insensitive user matching",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Users: []string{"BMBouter"}},
			eventType: "issues",
			users:     []string{"bmbouter"},
			want:      true,
		},
		{
			name:      "multiple trigger users, one matches",
			trigger:   &GitHubTrigger{Events: []string{"issues"}, Users: []string{"alice", "bmbouter"}},
			eventType: "issues",
			users:     []string{"bmbouter"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.trigger.Matches(tt.eventType, tt.action, tt.repo, tt.branch, tt.labels, tt.users)
			if got != tt.want {
				t.Errorf("Matches(%q, %q, %q, %q, %v, %v) = %v, want %v",
					tt.eventType, tt.action, tt.repo, tt.branch, tt.labels, tt.users, got, tt.want)
			}
		})
	}
}

func TestGitLabTriggerMatches(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *GitLabTrigger
		eventType string
		action    string
		project   string
		branch    string
		labels    []string
		want      bool
	}{
		{
			name:      "nil trigger",
			trigger:   nil,
			eventType: "merge_request",
			want:      false,
		},
		{
			name:      "matching merge_request event",
			trigger:   &GitLabTrigger{Events: []string{"merge_request"}},
			eventType: "merge_request",
			want:      true,
		},
		{
			name:      "non-matching event type",
			trigger:   &GitLabTrigger{Events: []string{"merge_request"}},
			eventType: "issue",
			want:      false,
		},
		{
			name:      "matching event with action filter",
			trigger:   &GitLabTrigger{Events: []string{"merge_request"}, Actions: []string{"opened", "merged"}},
			eventType: "merge_request",
			action:    "opened",
			want:      true,
		},
		{
			name:      "non-matching action",
			trigger:   &GitLabTrigger{Events: []string{"merge_request"}, Actions: []string{"opened"}},
			eventType: "merge_request",
			action:    "closed",
			want:      false,
		},
		{
			name:      "matching project filter",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Projects: []string{"group/project"}},
			eventType: "push",
			project:   "group/project",
			want:      true,
		},
		{
			name:      "non-matching project filter",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Projects: []string{"group/project"}},
			eventType: "push",
			project:   "other/project",
			want:      false,
		},
		{
			name:      "wildcard project filter",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Projects: []string{"*"}},
			eventType: "push",
			project:   "any/project",
			want:      true,
		},
		{
			name:      "matching branch filter",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Branches: []string{"main", "develop"}},
			eventType: "push",
			branch:    "main",
			want:      true,
		},
		{
			name:      "non-matching branch filter",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Branches: []string{"main"}},
			eventType: "push",
			branch:    "feature-x",
			want:      false,
		},
		{
			name:      "empty filters match all",
			trigger:   &GitLabTrigger{Events: []string{"merge_request"}},
			eventType: "merge_request",
			action:    "any",
			project:   "any/project",
			branch:    "any-branch",
			want:      true,
		},
		{
			name: "all filters combined - match",
			trigger: &GitLabTrigger{
				Events:   []string{"merge_request"},
				Actions:  []string{"opened"},
				Projects: []string{"group/project"},
				Branches: []string{"main"},
			},
			eventType: "merge_request",
			action:    "opened",
			project:   "group/project",
			branch:    "main",
			want:      true,
		},
		{
			name: "all filters combined - project mismatch",
			trigger: &GitLabTrigger{
				Events:   []string{"merge_request"},
				Actions:  []string{"opened"},
				Projects: []string{"group/project"},
				Branches: []string{"main"},
			},
			eventType: "merge_request",
			action:    "opened",
			project:   "other/project",
			branch:    "main",
			want:      false,
		},
		{
			name:      "case insensitive event matching",
			trigger:   &GitLabTrigger{Events: []string{"Push"}},
			eventType: "push",
			want:      true,
		},
		// Label filter tests
		{
			name:      "trigger with labels + event with matching label",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"ready-for-dev"}},
			eventType: "issue",
			labels:    []string{"bug", "ready-for-dev"},
			want:      true,
		},
		{
			name:      "trigger with labels + event with no labels",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"ready-for-dev"}},
			eventType: "issue",
			labels:    nil,
			want:      false,
		},
		{
			name:      "trigger with labels + event with different labels",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"ready-for-dev"}},
			eventType: "issue",
			labels:    []string{"bug", "enhancement"},
			want:      false,
		},
		{
			name:      "trigger with no labels + event with any labels",
			trigger:   &GitLabTrigger{Events: []string{"issue"}},
			eventType: "issue",
			labels:    []string{"bug", "enhancement"},
			want:      true,
		},
		{
			name:      "case insensitive label matching",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"Ready-For-Dev"}},
			eventType: "issue",
			labels:    []string{"ready-for-dev"},
			want:      true,
		},
		{
			name:      "trigger with multiple labels + event matches one",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"ready-for-dev", "approved"}},
			eventType: "issue",
			labels:    []string{"approved"},
			want:      true,
		},
		{
			name:      "trigger with labels + empty labels slice",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Labels: []string{"ready-for-dev"}},
			eventType: "issue",
			labels:    []string{},
			want:      false,
		},
		// GitLab-specific event types
		{
			name:      "pipeline event",
			trigger:   &GitLabTrigger{Events: []string{"pipeline"}},
			eventType: "pipeline",
			want:      true,
		},
		{
			name:      "comment event",
			trigger:   &GitLabTrigger{Events: []string{"comment"}},
			eventType: "comment",
			want:      true,
		},
		// Path-based project matching (GitLab style)
		{
			name:      "multi-level project path",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Projects: []string{"group/subgroup/project"}},
			eventType: "push",
			project:   "group/subgroup/project",
			want:      true,
		},
		{
			name:      "pattern matching for project",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Projects: []string{"group/*"}},
			eventType: "push",
			project:   "group/any-project",
			want:      true,
		},
		// GitLab actions
		{
			name:      "gitlab labeled action",
			trigger:   &GitLabTrigger{Events: []string{"issue"}, Actions: []string{"labeled"}},
			eventType: "issue",
			action:    "labeled",
			want:      true,
		},
		{
			name:      "gitlab pushed action",
			trigger:   &GitLabTrigger{Events: []string{"push"}, Actions: []string{"pushed"}},
			eventType: "push",
			action:    "pushed",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.trigger.Matches(tt.eventType, tt.action, tt.project, tt.branch, tt.labels)
			if got != tt.want {
				t.Errorf("Matches(%q, %q, %q, %q, %v) = %v, want %v",
					tt.eventType, tt.action, tt.project, tt.branch, tt.labels, got, tt.want)
			}
		})
	}
}
