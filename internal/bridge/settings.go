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
