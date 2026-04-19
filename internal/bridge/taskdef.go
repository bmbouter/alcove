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
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// PluginSpec declares a Claude Code plugin to install for an agent.
type PluginSpec struct {
	Name   string `json:"name" yaml:"name"`                         // Plugin name (e.g., "code-review")
	Source string `json:"source,omitempty" yaml:"source,omitempty"` // "claude-plugins-official", git URL, or empty (marketplace default)
	Ref    string `json:"ref,omitempty" yaml:"ref,omitempty"`       // Branch/tag for git sources
}

// DevContainerSpec declares a dev container image to run as a sidecar.
type DevContainerSpec struct {
	Image         string `json:"image" yaml:"image"`
	NetworkAccess string `json:"network_access,omitempty" yaml:"network_access,omitempty"`
}

// CIGate configures Bridge-driven CI monitoring for PRs created by a task.
type CIGate struct {
	MaxRetries int `json:"max_retries" yaml:"max_retries"`
	Timeout    int `json:"timeout" yaml:"timeout"` // seconds to wait for CI, default 900
}

// TaskDefinition represents an agent definition defined in a YAML file within an agent repo.
type TaskDefinition struct {
	ID             string                   `json:"id"`
	Name           string                   `json:"name" yaml:"name"`
	Description    string                   `json:"description" yaml:"description"`
	Prompt         string                   `json:"prompt,omitempty" yaml:"prompt"`
	Executable     *internal.ExecutableSpec `json:"executable,omitempty" yaml:"executable"`
	Repos          []internal.RepoSpec      `json:"repos,omitempty" yaml:"repos"`
	Provider       string                   `json:"provider,omitempty" yaml:"provider"`
	Model          string                   `json:"model,omitempty" yaml:"model"`
	Timeout        int                      `json:"timeout,omitempty" yaml:"timeout"`
	BudgetUSD      float64                  `json:"budget_usd,omitempty" yaml:"budget_usd"`
	Debug          bool                     `json:"debug,omitempty" yaml:"debug"`
	Profiles       []string                 `json:"profiles,omitempty" yaml:"profiles"`
	Plugins        []PluginSpec             `json:"plugins,omitempty" yaml:"plugins"`
	Tools          map[string]ToolConfig    `json:"tools,omitempty" yaml:"tools"`
	Credentials    map[string]string        `json:"credentials,omitempty" yaml:"credentials"`
	Schedule       *TaskDefSchedule         `json:"schedule,omitempty" yaml:"schedule"`
	Trigger        *EventTrigger            `json:"trigger,omitempty" yaml:"trigger"`
	CIGate         *CIGate                  `json:"ci_gate,omitempty" yaml:"ci_gate"`
	DirectOutbound bool                     `json:"direct_outbound,omitempty" yaml:"direct_outbound"`
	DevContainer   *DevContainerSpec       `json:"dev_container,omitempty" yaml:"dev_container"`

	// Metadata (not from YAML).
	TeamID       string     `json:"team_id,omitempty"`
	SourceRepo   string     `json:"source_repo"`
	SourceFile   string     `json:"source_file"`
	SourceKey    string     `json:"source_key"`
	RawYAML      string     `json:"raw_yaml,omitempty"`
	SyncError    string     `json:"sync_error,omitempty"`
	LastSynced   time.Time  `json:"last_synced"`
	NextRun      *time.Time `json:"next_run,omitempty"`
	LastRun      *time.Time `json:"last_run,omitempty"`
	RepoDisabled bool       `json:"repo_disabled"`
}

// TaskDefSchedule defines an optional cron schedule for an agent definition.
type TaskDefSchedule struct {
	Cron    string `json:"cron" yaml:"cron"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
}

// ParseTaskDefinition parses a YAML byte slice into a TaskDefinition and
// validates required fields.
func ParseTaskDefinition(data []byte) (*TaskDefinition, error) {
	var td TaskDefinition
	if err := yaml.Unmarshal(data, &td); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	if td.Name == "" {
		return nil, fmt.Errorf("agent definition missing required field: name")
	}

	// Validation: either prompt or executable.url must be set, but not both
	hasPrompt := td.Prompt != ""
	hasExecutable := td.Executable != nil && td.Executable.URL != ""

	if !hasPrompt && !hasExecutable {
		return nil, fmt.Errorf("agent definition must have either 'prompt' or 'executable.url' field")
	}
	if hasPrompt && hasExecutable {
		return nil, fmt.Errorf("agent definition cannot have both 'prompt' and 'executable' fields")
	}

	if td.Schedule != nil {
		if td.Schedule.Cron == "" {
			return nil, fmt.Errorf("schedule block present but cron expression is empty")
		}
		if _, err := ParseCron(td.Schedule.Cron); err != nil {
			return nil, fmt.Errorf("invalid cron expression in schedule: %w", err)
		}
	}

	if td.Trigger != nil {
		if td.Trigger.GitHub != nil && len(td.Trigger.GitHub.Events) == 0 {
			return nil, fmt.Errorf("trigger.github block present but events list is empty")
		}
	}

	if td.DevContainer != nil && td.DevContainer.Image == "" {
		return nil, fmt.Errorf("dev_container block present but image is empty")
	}
	if td.DevContainer != nil && td.DevContainer.NetworkAccess != "" {
		if td.DevContainer.NetworkAccess != "internal" && td.DevContainer.NetworkAccess != "external" {
			return nil, fmt.Errorf("dev_container.network_access must be \"internal\" or \"external\", got %q", td.DevContainer.NetworkAccess)
		}
	}
	// Default network_access to "internal" if not set.
	if td.DevContainer != nil && td.DevContainer.NetworkAccess == "" {
		td.DevContainer.NetworkAccess = "internal"
	}

	// Validate repos: each must have a non-empty URL, derive Name from URL if not provided, check for duplicates.
	if len(td.Repos) > 0 {
		namesSeen := make(map[string]bool)
		for i := range td.Repos {
			if td.Repos[i].URL == "" {
				return nil, fmt.Errorf("repos[%d] has empty URL", i)
			}
			if td.Repos[i].Name == "" {
				td.Repos[i].Name = repoNameFromURL(td.Repos[i].URL)
			}
			if namesSeen[td.Repos[i].Name] {
				return nil, fmt.Errorf("duplicate repo name %q", td.Repos[i].Name)
			}
			namesSeen[td.Repos[i].Name] = true
		}
	}

	return &td, nil
}

// ToTaskRequest converts a TaskDefinition to a TaskRequest suitable for
// dispatching via the Dispatcher.
func (td *TaskDefinition) ToTaskRequest() TaskRequest {
	return TaskRequest{
		Prompt:         td.Prompt,
		Executable:     td.Executable,
		Repos:          td.Repos,
		Provider:       td.Provider,
		Timeout:        td.Timeout,
		Tools:          td.Tools,
		Profiles:       td.Profiles,
		Model:          td.Model,
		Budget:         td.BudgetUSD,
		Debug:          td.Debug,
		Plugins:        td.Plugins,
		Credentials:    td.Credentials,
		DirectOutbound: td.DirectOutbound,
		DevContainer:   td.DevContainer,
	}
}

// AgentDefStore manages agent definitions in PostgreSQL.
type AgentDefStore struct {
	db *pgxpool.Pool
}

// NewAgentDefStore creates an AgentDefStore with the given database pool.
func NewAgentDefStore(db *pgxpool.Pool) *AgentDefStore {
	return &AgentDefStore{db: db}
}

// ListAgentDefinitions returns agent definitions for the given team, with parsed data and schedule info.
func (s *AgentDefStore) ListAgentDefinitions(ctx context.Context, teamID string) ([]TaskDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT td.id, td.name, td.description, td.source_repo, td.source_file, td.source_key,
		       td.parsed, td.has_schedule, td.sync_error, td.last_synced,
		       td.created_at, td.updated_at,
		       s.next_run, s.last_run
		FROM agent_definitions td
		LEFT JOIN schedules s ON s.source_key = td.source_key AND s.source = 'yaml'
		WHERE td.team_id = $1
		ORDER BY td.name ASC
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying agent definitions: %w", err)
	}
	defer rows.Close()

	var defs []TaskDefinition
	for rows.Next() {
		var td TaskDefinition
		var parsedJSON []byte
		var hasSchedule bool
		var syncError *string
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
			&td.SourceKey, &parsedJSON, &hasSchedule, &syncError, &td.LastSynced,
			&createdAt, &updatedAt,
			&td.NextRun, &td.LastRun,
		); err != nil {
			return nil, fmt.Errorf("scanning agent definition: %w", err)
		}

		if syncError != nil {
			td.SyncError = *syncError
		}

		// Deserialize parsed JSONB for profiles, schedule, and trigger data.
		if parsedJSON != nil {
			var parsed TaskDefinition
			if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
				td.Prompt = parsed.Prompt
				td.Executable = parsed.Executable
				td.Repos = parsed.Repos
				td.Provider = parsed.Provider
				td.Model = parsed.Model
				td.Timeout = parsed.Timeout
				td.BudgetUSD = parsed.BudgetUSD
				td.Debug = parsed.Debug
				td.Profiles = parsed.Profiles
				td.Tools = parsed.Tools
				td.Schedule = parsed.Schedule
				td.Trigger = parsed.Trigger
				td.Plugins = parsed.Plugins
				td.Credentials = parsed.Credentials
				td.DirectOutbound = parsed.DirectOutbound
				td.CIGate = parsed.CIGate
				td.DevContainer = parsed.DevContainer
			}
		}

		defs = append(defs, td)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating agent definitions: %w", err)
	}

	if defs == nil {
		defs = []TaskDefinition{}
	}
	return defs, nil
}

// GetAgentDefinition retrieves a single agent definition by ID, scoped to the given team.
func (s *AgentDefStore) GetAgentDefinition(ctx context.Context, id, teamID string) (*TaskDefinition, error) {
	var td TaskDefinition
	var parsedJSON []byte
	var syncError *string
	var hasSchedule bool
	var createdAt, updatedAt time.Time

	err := s.db.QueryRow(ctx, `
		SELECT td.id, td.name, td.description, td.source_repo, td.source_file, td.source_key,
		       td.raw_yaml, td.parsed, td.has_schedule, td.sync_error, td.last_synced,
		       td.created_at, td.updated_at,
		       s.next_run, s.last_run
		FROM agent_definitions td
		LEFT JOIN schedules s ON s.source_key = td.source_key AND s.source = 'yaml'
		WHERE (td.id = $1 OR td.name = $1) AND td.team_id = $2
	`, id, teamID).Scan(
		&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
		&td.SourceKey, &td.RawYAML, &parsedJSON, &hasSchedule, &syncError,
		&td.LastSynced, &createdAt, &updatedAt,
		&td.NextRun, &td.LastRun,
	)
	if err != nil {
		return nil, fmt.Errorf("querying agent definition %s: %w", id, err)
	}

	if syncError != nil {
		td.SyncError = *syncError
	}

	// Unmarshal the parsed JSON back into the struct fields.
	if parsedJSON != nil {
		var parsed TaskDefinition
		if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
			td.Prompt = parsed.Prompt
			td.Executable = parsed.Executable
			td.Repos = parsed.Repos
			td.Provider = parsed.Provider
			td.Model = parsed.Model
			td.Timeout = parsed.Timeout
			td.BudgetUSD = parsed.BudgetUSD
			td.Debug = parsed.Debug
			td.Profiles = parsed.Profiles
			td.Tools = parsed.Tools
			td.Schedule = parsed.Schedule
			td.Trigger = parsed.Trigger
			td.Plugins = parsed.Plugins
			td.Credentials = parsed.Credentials
			td.DirectOutbound = parsed.DirectOutbound
			td.CIGate = parsed.CIGate
			td.DevContainer = parsed.DevContainer
		}
	}

	return &td, nil
}

// UpsertAgentDefinition inserts or updates a agent definition by source_key.
func (s *AgentDefStore) UpsertAgentDefinition(ctx context.Context, def *TaskDefinition) error {
	if def.ID == "" {
		def.ID = uuid.New().String()
	}

	parsedJSON, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling parsed agent definition: %w", err)
	}

	hasSchedule := def.Schedule != nil
	now := time.Now().UTC()

	_, err = s.db.Exec(ctx, `
		INSERT INTO agent_definitions (id, name, description, source_repo, source_file,
		    source_key, raw_yaml, parsed, has_schedule, sync_error, last_synced, team_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (source_key, team_id) DO UPDATE SET
		    name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    source_repo = EXCLUDED.source_repo,
		    source_file = EXCLUDED.source_file,
		    raw_yaml = EXCLUDED.raw_yaml,
		    parsed = EXCLUDED.parsed,
		    has_schedule = EXCLUDED.has_schedule,
		    sync_error = EXCLUDED.sync_error,
		    last_synced = EXCLUDED.last_synced,
		    team_id = EXCLUDED.team_id,
		    updated_at = EXCLUDED.updated_at
	`, def.ID, def.Name, def.Description, def.SourceRepo, def.SourceFile,
		def.SourceKey, def.RawYAML, parsedJSON, hasSchedule, nilIfEmpty(def.SyncError),
		now, def.TeamID, now, now,
	)
	if err != nil {
		return fmt.Errorf("upserting agent definition: %w", err)
	}

	return nil
}

// DeleteAgentDefinitionsByRepo removes all agent definitions from a given repo URL and team.
func (s *AgentDefStore) DeleteAgentDefinitionsByRepo(ctx context.Context, repoURL, teamID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM agent_definitions WHERE source_repo = $1 AND team_id = $2`, repoURL, teamID)
	if err != nil {
		return fmt.Errorf("deleting agent definitions for repo %s: %w", repoURL, err)
	}
	return nil
}

// ListAgentDefinitionsByRepo returns all agent definitions from a given repo URL and team.
func (s *AgentDefStore) ListAgentDefinitionsByRepo(ctx context.Context, repoURL, teamID string) ([]TaskDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key,
		       has_schedule, sync_error, last_synced, created_at, updated_at, parsed
		FROM agent_definitions WHERE source_repo = $1 AND team_id = $2
		ORDER BY name ASC
	`, repoURL, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying agent definitions for repo %s: %w", repoURL, err)
	}
	defer rows.Close()

	var defs []TaskDefinition
	for rows.Next() {
		var td TaskDefinition
		var hasSchedule bool
		var syncError *string
		var createdAt, updatedAt time.Time
		var parsedJSON []byte

		if err := rows.Scan(
			&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
			&td.SourceKey, &hasSchedule, &syncError, &td.LastSynced,
			&createdAt, &updatedAt, &parsedJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning agent definition: %w", err)
		}

		if syncError != nil {
			td.SyncError = *syncError
		}
		if parsedJSON != nil {
			var parsed TaskDefinition
			if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
				td.Prompt = parsed.Prompt
				td.Executable = parsed.Executable
				td.Repos = parsed.Repos
				td.Provider = parsed.Provider
				td.Model = parsed.Model
				td.Timeout = parsed.Timeout
				td.BudgetUSD = parsed.BudgetUSD
				td.Debug = parsed.Debug
				td.Profiles = parsed.Profiles
				td.Tools = parsed.Tools
				td.Schedule = parsed.Schedule
				td.Trigger = parsed.Trigger
				td.Plugins = parsed.Plugins
				td.Credentials = parsed.Credentials
				td.DirectOutbound = parsed.DirectOutbound
				td.CIGate = parsed.CIGate
				td.DevContainer = parsed.DevContainer
			}
		}
		defs = append(defs, td)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating agent definitions: %w", err)
	}

	if defs == nil {
		defs = []TaskDefinition{}
	}
	return defs, nil
}

// PluginBundles maps bundle names to their constituent plugins.
var PluginBundles = map[string][]PluginSpec{
	"sdlc-go": {
		{Name: "code-review", Source: "claude-plugins-official"},
		{Name: "gopls-lsp", Source: "claude-plugins-official"},
		{Name: "commit-commands", Source: "claude-plugins-official"},
	},
	"sdlc-python": {
		{Name: "code-review", Source: "claude-plugins-official"},
		{Name: "commit-commands", Source: "claude-plugins-official"},
	},
	"sdlc-typescript": {
		{Name: "code-review", Source: "claude-plugins-official"},
		{Name: "commit-commands", Source: "claude-plugins-official"},
	},
	"content": {
		{Name: "claude-md-management", Source: "claude-plugins-official"},
	},
}

// ResolvePluginBundles expands any bundle references in the plugin list.
// A bundle is referenced by setting Source to "bundle".
func ResolvePluginBundles(plugins []PluginSpec) []PluginSpec {
	var resolved []PluginSpec
	seen := make(map[string]bool) // deduplicate by name

	for _, p := range plugins {
		if p.Source == "bundle" {
			if bundle, ok := PluginBundles[p.Name]; ok {
				for _, bp := range bundle {
					if !seen[bp.Name] {
						resolved = append(resolved, bp)
						seen[bp.Name] = true
					}
				}
			}
			continue
		}
		if !seen[p.Name] {
			resolved = append(resolved, p)
			seen[p.Name] = true
		}
	}
	return resolved
}

// repoNameFromURL derives a short repo name from a URL by taking the
// basename and stripping any ".git" suffix.
func repoNameFromURL(url string) string {
	base := path.Base(url)
	return strings.TrimSuffix(base, ".git")
}

// nilIfEmpty returns nil if the string is empty, otherwise a pointer to it.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
