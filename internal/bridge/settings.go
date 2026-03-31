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

// SystemLLMSettings holds the database-stored LLM configuration.
type SystemLLMSettings struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Region       string `json:"region,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	CredentialID string `json:"credential_id,omitempty"` // references provider_credentials
}

// EffectiveSystemLLM represents the resolved LLM configuration with source tracking.
// Each field includes a corresponding source indicator ("env", "database", or "default").
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

// GetSystemLLM retrieves the stored system LLM settings from the database.
// Returns nil and an error if no settings are stored.
func (s *SettingsStore) GetSystemLLM(ctx context.Context) (*SystemLLMSettings, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = 'system_llm'").Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("system LLM settings not found: %w", err)
	}
	var settings SystemLLMSettings
	if err := json.Unmarshal(value, &settings); err != nil {
		return nil, fmt.Errorf("unmarshaling system LLM settings: %w", err)
	}
	return &settings, nil
}

// SetSystemLLM stores or updates the system LLM settings in the database.
func (s *SettingsStore) SetSystemLLM(ctx context.Context, settings *SystemLLMSettings) error {
	value, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshaling system LLM settings: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at) VALUES ('system_llm', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = $2
	`, value, time.Now().UTC())
	return err
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

// GetSystemTaskRepos returns the system-wide task repos.
func (s *SettingsStore) GetSystemTaskRepos(ctx context.Context) ([]SkillRepo, error) {
	var value json.RawMessage
	err := s.db.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = 'task_repos'").Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("system task repos not found: %w", err)
	}
	var repos []SkillRepo
	if err := json.Unmarshal(value, &repos); err != nil {
		return nil, fmt.Errorf("unmarshaling system task repos: %w", err)
	}
	return repos, nil
}

// SetSystemTaskRepos saves the system-wide task repos.
func (s *SettingsStore) SetSystemTaskRepos(ctx context.Context, repos []SkillRepo) error {
	value, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling system task repos: %w", err)
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at) VALUES ('task_repos', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = $1, updated_at = $2
	`, value, time.Now().UTC())
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

// ResolveEffective merges DB settings with env var overrides.
// Env vars always win. Returns the effective config with source tracking.
func (s *SettingsStore) ResolveEffective(ctx context.Context, cfg *Config) *EffectiveSystemLLM {
	eff := &EffectiveSystemLLM{}

	// Start with DB values.
	dbSettings, _ := s.GetSystemLLM(ctx)
	if dbSettings != nil {
		eff.Provider = dbSettings.Provider
		eff.ProviderSrc = "database"
		eff.Model = dbSettings.Model
		eff.ModelSrc = "database"
		eff.Region = dbSettings.Region
		eff.RegionSrc = "database"
		eff.ProjectID = dbSettings.ProjectID
		eff.ProjectSrc = "database"
	}

	// Env vars override.
	if cfg.SystemLLM.Provider != "" {
		eff.Provider = cfg.SystemLLM.Provider
		eff.ProviderSrc = "env"
	}
	if cfg.SystemLLM.Model != "" {
		eff.Model = cfg.SystemLLM.Model
		eff.ModelSrc = "env"
	}
	if v := envOrDefault("BRIDGE_LLM_REGION", ""); v != "" {
		eff.Region = v
		eff.RegionSrc = "env"
	}
	if v := envOrDefault("BRIDGE_LLM_PROJECT", ""); v != "" {
		eff.ProjectID = v
		eff.ProjectSrc = "env"
	}

	// Defaults.
	if eff.Model == "" {
		eff.Model = "claude-sonnet-4-20250514"
		eff.ModelSrc = "default"
	}
	if eff.Region == "" {
		eff.Region = "us-east5"
		eff.RegionSrc = "default"
	}

	eff.Configured = eff.Provider != ""
	return eff
}
