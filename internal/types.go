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

// Package internal contains shared types for Alcove components.
package internal

import "time"

// Task represents a unit of work dispatched to a Skiff pod.
type Task struct {
	ID       string            `json:"id"`
	Prompt   string            `json:"prompt"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
	Provider string            `json:"provider"`
	Scope    Scope             `json:"scope"`
	Timeout  time.Duration     `json:"timeout"`
	Budget   float64           `json:"budget_usd,omitempty"`
	Model    string            `json:"model,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

// Scope defines what external operations a Skiff pod is authorized to perform.
type Scope struct {
	Services map[string]ServiceScope `json:"services"`
}

// ServiceScope defines the authorized operations for a specific service.
type ServiceScope struct {
	Repos      []string `json:"repos,omitempty"`
	Operations []string `json:"operations"`
}

// Session represents a completed or in-progress task execution record.
type Session struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	Submitter      string     `json:"submitter"`
	Prompt         string     `json:"prompt"`
	Repo           string     `json:"repo,omitempty"`
	Provider       string     `json:"provider"`
	Scope          Scope      `json:"scope"`
	Status         string     `json:"status"` // running, completed, timeout, cancelled, error
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	Duration       string     `json:"duration,omitempty"`
	Artifacts      []Artifact `json:"artifacts,omitempty"`
	ParentID       string     `json:"parent_id,omitempty"`
	TaskName       string     `json:"task_name,omitempty"`
	TriggerContext string     `json:"trigger_context,omitempty"` // keep for backward compat
	TriggerType    string     `json:"trigger_type,omitempty"`
	TriggerRef     string     `json:"trigger_ref,omitempty"`
	TeamID         string     `json:"team_id,omitempty"`
}

// Artifact represents an output produced by a task (PR, commit, etc.).
type Artifact struct {
	Type string `json:"type"` // pr, commit, file
	URL  string `json:"url,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// TranscriptEvent is a single event from Claude Code's stream-json output.
type TranscriptEvent struct {
	Type      string    `json:"type"`
	Content   any       `json:"content,omitempty"`
	Tool      string    `json:"tool,omitempty"`
	Input     any       `json:"input,omitempty"`
	Output    any       `json:"output,omitempty"`
	Timestamp time.Time `json:"ts"`
}

// ProxyLogEntry records a single Gate request/response.
type ProxyLogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	Service    string    `json:"service,omitempty"`
	Operation  string    `json:"operation,omitempty"`
	Decision   string    `json:"decision"` // allow, deny
	StatusCode int       `json:"status_code,omitempty"`
	SessionID  string    `json:"session_id"`
}

// Provider holds LLM provider configuration.
type Provider struct {
	Name            string `json:"name"`
	Type            string `json:"type"` // google-vertex, anthropic, claude-pro
	Model           string `json:"model,omitempty"`
	MaxBudgetUSD    float64 `json:"max_budget_usd,omitempty"`
}
