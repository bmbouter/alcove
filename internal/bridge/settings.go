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

	"github.com/jackc/pgx/v5/pgxpool"
)

// EffectiveSystemLLM represents the resolved LLM configuration with source tracking.
// Each field includes a corresponding source indicator ("config" or "default").
type EffectiveSystemLLM struct {
	Provider    string `json:"provider"`
	ProviderSrc string `json:"provider_source"`
	Model       string `json:"model"`
	ModelSrc    string `json:"model_source"`
	Region      string `json:"region"`
	RegionSrc   string `json:"region_source"`
	ProjectID   string `json:"project_id"`
	ProjectSrc  string `json:"project_id_source"`
	Configured  bool   `json:"configured"`
}

// SettingsStore manages system settings in PostgreSQL.
type SettingsStore struct {
	db *pgxpool.Pool
}

// NewSettingsStore creates a SettingsStore with the given database pool.
func NewSettingsStore(db *pgxpool.Pool) *SettingsStore {
	return &SettingsStore{db: db}
}

// SkillRepo represents a git repository containing Claude Code skills/agents.
type SkillRepo struct {
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`  // branch/tag/commit, default: main
	Name string `json:"name,omitempty"` // display name
}

// GetSystemSkillRepos returns the system-wide skill repos.
func (s *SettingsStore) GetSystemSkillRepos(ctx context.Context) ([]SkillRepo, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = 'skill_repos'").Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("system skill repos not found: %w", err)
	}
	var repos []SkillRepo
	if err := json.Unmarshal(value, &repos); err != nil {
		return nil, fmt.Errorf("unmarshaling system skill repos: %w", err)
	}
	return repos, nil
}

// SetSystemSkillRepos saves the system-wide skill repos.
func (s *SettingsStore) SetSystemSkillRepos(ctx context.Context, repos []SkillRepo) error {
	value, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling system skill repos: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at) VALUES ('skill_repos', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = $2
	`, value, time.Now().UTC())
	return err
}

// GetUserSkillRepos returns a user's personal skill repos.
func (s *SettingsStore) GetUserSkillRepos(ctx context.Context, username string) ([]SkillRepo, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM user_settings WHERE username = $1 AND key = 'skill_repos'", username).Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("user skill repos not found: %w", err)
	}
	var repos []SkillRepo
	if err := json.Unmarshal(value, &repos); err != nil {
		return nil, fmt.Errorf("unmarshaling user skill repos: %w", err)
	}
	return repos, nil
}

// SetUserSkillRepos saves a user's personal skill repos.
func (s *SettingsStore) SetUserSkillRepos(ctx context.Context, username string, repos []SkillRepo) error {
	value, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling user skill repos: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO user_settings (username, key, value, updated_at) VALUES ($1, 'skill_repos', $2, $3)
		ON CONFLICT (username, key) DO UPDATE SET value = $2, updated_at = $3
	`, username, value, time.Now().UTC())
	return err
}

// GetUserTaskRepos returns a user's personal task repos.
func (s *SettingsStore) GetUserTaskRepos(ctx context.Context, username string) ([]SkillRepo, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM user_settings WHERE username = $1 AND key = 'task_repos'", username).Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("user task repos not found: %w", err)
	}
	var repos []SkillRepo
	if err := json.Unmarshal(value, &repos); err != nil {
		return nil, fmt.Errorf("unmarshaling user task repos: %w", err)
	}
	return repos, nil
}

// SetUserTaskRepos saves a user's personal task repos.
func (s *SettingsStore) SetUserTaskRepos(ctx context.Context, username string, repos []SkillRepo) error {
	value, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling user task repos: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO user_settings (username, key, value, updated_at) VALUES ($1, 'task_repos', $2, $3)
		ON CONFLICT (username, key) DO UPDATE SET value = $2, updated_at = $3
	`, username, value, time.Now().UTC())
	return err
}

// GetWebhookSecret retrieves the webhook secret from the system_settings table.
func (s *SettingsStore) GetWebhookSecret(ctx context.Context) (string, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = 'webhook_secret'").Scan(&value)
	if err != nil {
		return "", fmt.Errorf("webhook secret not found: %w", err)
	}
	var secret string
	if err := json.Unmarshal(value, &secret); err != nil {
		return "", fmt.Errorf("unmarshaling webhook secret: %w", err)
	}
	return secret, nil
}

// SetWebhookSecret stores or updates the webhook secret in the system_settings table.
func (s *SettingsStore) SetWebhookSecret(ctx context.Context, secret string) error {
	value, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("marshaling webhook secret: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at) VALUES ('webhook_secret', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = $2
	`, value, time.Now().UTC())
	return err
}

// ResolveEffectiveLLM reads LLM configuration from Config (config file + env vars)
// and returns the effective config with source tracking.
func ResolveEffectiveLLM(cfg *Config) *EffectiveSystemLLM {
	eff := &EffectiveSystemLLM{}
	llm := cfg.SystemLLM

	if llm.Provider != "" {
		eff.Provider = llm.Provider
		eff.ProviderSrc = "config"
	}
	if llm.Model != "" {
		eff.Model = llm.Model
		eff.ModelSrc = "config"
	} else {
		eff.Model = "claude-sonnet-4-20250514"
		eff.ModelSrc = "default"
	}
	if llm.Region != "" {
		eff.Region = llm.Region
		eff.RegionSrc = "config"
	} else if llm.Provider == "google-vertex" {
		eff.Region = "us-east5"
		eff.RegionSrc = "default"
	}
	if llm.ProjectID != "" {
		eff.ProjectID = llm.ProjectID
		eff.ProjectSrc = "config"
	}

	eff.Configured = eff.Provider != ""
	return eff
}
