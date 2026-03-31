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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// TaskDefinition represents a task defined in a YAML file within a task repo.
type TaskDefinition struct {
	ID          string                `json:"id"`
	Name        string                `json:"name" yaml:"name"`
	Description string                `json:"description" yaml:"description"`
	Prompt      string                `json:"prompt" yaml:"prompt"`
	Repo        string                `json:"repo,omitempty" yaml:"repo"`
	Provider    string                `json:"provider,omitempty" yaml:"provider"`
	Model       string                `json:"model,omitempty" yaml:"model"`
	Timeout     int                   `json:"timeout,omitempty" yaml:"timeout"`
	BudgetUSD   float64               `json:"budget_usd,omitempty" yaml:"budget_usd"`
	Debug       bool                  `json:"debug,omitempty" yaml:"debug"`
	Profiles    []string              `json:"profiles,omitempty" yaml:"profiles"`
	Tools       map[string]ToolConfig `json:"tools,omitempty" yaml:"tools"`
	Schedule    *TaskDefSchedule      `json:"schedule,omitempty" yaml:"schedule"`
	Trigger     *EventTrigger         `json:"trigger,omitempty" yaml:"trigger"`

	// Metadata (not from YAML).
	SourceRepo string    `json:"source_repo"`
	SourceFile string    `json:"source_file"`
	SourceKey  string    `json:"source_key"`
	RawYAML    string    `json:"raw_yaml,omitempty"`
	SyncError  string    `json:"sync_error,omitempty"`
	LastSynced time.Time `json:"last_synced"`
}

// TaskDefSchedule defines an optional cron schedule for a task definition.
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
		return nil, fmt.Errorf("task definition missing required field: name")
	}
	if td.Prompt == "" {
		return nil, fmt.Errorf("task definition missing required field: prompt")
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

	return &td, nil
}

// ToTaskRequest converts a TaskDefinition to a TaskRequest suitable for
// dispatching via the Dispatcher.
func (td *TaskDefinition) ToTaskRequest() TaskRequest {
	return TaskRequest{
		Prompt:   td.Prompt,
		Repo:     td.Repo,
		Provider: td.Provider,
		Timeout:  td.Timeout,
		Tools:    td.Tools,
		Profiles: td.Profiles,
		Model:    td.Model,
		Budget:   td.BudgetUSD,
		Debug:    td.Debug,
	}
}

// TaskDefStore manages task definitions in PostgreSQL.
type TaskDefStore struct {
	db *pgxpool.Pool
}

// NewTaskDefStore creates a TaskDefStore with the given database pool.
func NewTaskDefStore(db *pgxpool.Pool) *TaskDefStore {
	return &TaskDefStore{db: db}
}

// ListTaskDefinitions returns all task definitions, excluding raw_yaml for brevity.
func (s *TaskDefStore) ListTaskDefinitions(ctx context.Context) ([]TaskDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key,
		       has_schedule, sync_error, last_synced, created_at, updated_at
		FROM task_definitions
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("querying task definitions: %w", err)
	}
	defer rows.Close()

	var defs []TaskDefinition
	for rows.Next() {
		var td TaskDefinition
		var hasSchedule bool
		var syncError *string
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
			&td.SourceKey, &hasSchedule, &syncError, &td.LastSynced,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning task definition: %w", err)
		}

		if syncError != nil {
			td.SyncError = *syncError
		}
		defs = append(defs, td)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating task definitions: %w", err)
	}

	if defs == nil {
		defs = []TaskDefinition{}
	}
	return defs, nil
}

// GetTaskDefinition retrieves a single task definition by ID, including raw_yaml.
func (s *TaskDefStore) GetTaskDefinition(ctx context.Context, id string) (*TaskDefinition, error) {
	var td TaskDefinition
	var parsedJSON []byte
	var syncError *string
	var hasSchedule bool
	var createdAt, updatedAt time.Time

	err := s.db.QueryRow(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key,
		       raw_yaml, parsed, has_schedule, sync_error, last_synced,
		       created_at, updated_at
		FROM task_definitions WHERE id = $1
	`, id).Scan(
		&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
		&td.SourceKey, &td.RawYAML, &parsedJSON, &hasSchedule, &syncError,
		&td.LastSynced, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("querying task definition %s: %w", id, err)
	}

	if syncError != nil {
		td.SyncError = *syncError
	}

	// Unmarshal the parsed JSON back into the struct fields.
	if parsedJSON != nil {
		var parsed TaskDefinition
		if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
			td.Prompt = parsed.Prompt
			td.Repo = parsed.Repo
			td.Provider = parsed.Provider
			td.Model = parsed.Model
			td.Timeout = parsed.Timeout
			td.BudgetUSD = parsed.BudgetUSD
			td.Debug = parsed.Debug
			td.Profiles = parsed.Profiles
			td.Tools = parsed.Tools
			td.Schedule = parsed.Schedule
		}
	}

	return &td, nil
}

// UpsertTaskDefinition inserts or updates a task definition by source_key.
func (s *TaskDefStore) UpsertTaskDefinition(ctx context.Context, def *TaskDefinition) error {
	if def.ID == "" {
		def.ID = uuid.New().String()
	}

	parsedJSON, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling parsed task definition: %w", err)
	}

	hasSchedule := def.Schedule != nil
	now := time.Now().UTC()

	_, err = s.db.Exec(ctx, `
		INSERT INTO task_definitions (id, name, description, source_repo, source_file,
		    source_key, raw_yaml, parsed, has_schedule, sync_error, last_synced, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (source_key) DO UPDATE SET
		    name = EXCLUDED.name,
		    description = EXCLUDED.description,
		    source_repo = EXCLUDED.source_repo,
		    source_file = EXCLUDED.source_file,
		    raw_yaml = EXCLUDED.raw_yaml,
		    parsed = EXCLUDED.parsed,
		    has_schedule = EXCLUDED.has_schedule,
		    sync_error = EXCLUDED.sync_error,
		    last_synced = EXCLUDED.last_synced,
		    updated_at = EXCLUDED.updated_at
	`, def.ID, def.Name, def.Description, def.SourceRepo, def.SourceFile,
		def.SourceKey, def.RawYAML, parsedJSON, hasSchedule, nilIfEmpty(def.SyncError),
		now, now, now,
	)
	if err != nil {
		return fmt.Errorf("upserting task definition: %w", err)
	}

	return nil
}

// DeleteTaskDefinitionsByRepo removes all task definitions from a given repo URL.
func (s *TaskDefStore) DeleteTaskDefinitionsByRepo(ctx context.Context, repoURL string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM task_definitions WHERE source_repo = $1`, repoURL)
	if err != nil {
		return fmt.Errorf("deleting task definitions for repo %s: %w", repoURL, err)
	}
	return nil
}

// ListTaskDefinitionsByRepo returns all task definitions from a given repo URL.
func (s *TaskDefStore) ListTaskDefinitionsByRepo(ctx context.Context, repoURL string) ([]TaskDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key,
		       has_schedule, sync_error, last_synced, created_at, updated_at
		FROM task_definitions WHERE source_repo = $1
		ORDER BY name ASC
	`, repoURL)
	if err != nil {
		return nil, fmt.Errorf("querying task definitions for repo %s: %w", repoURL, err)
	}
	defer rows.Close()

	var defs []TaskDefinition
	for rows.Next() {
		var td TaskDefinition
		var hasSchedule bool
		var syncError *string
		var createdAt, updatedAt time.Time

		if err := rows.Scan(
			&td.ID, &td.Name, &td.Description, &td.SourceRepo, &td.SourceFile,
			&td.SourceKey, &hasSchedule, &syncError, &td.LastSynced,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning task definition: %w", err)
		}

		if syncError != nil {
			td.SyncError = *syncError
		}
		defs = append(defs, td)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating task definitions: %w", err)
	}

	if defs == nil {
		defs = []TaskDefinition{}
	}
	return defs, nil
}

// nilIfEmpty returns nil if the string is empty, otherwise a pointer to it.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
