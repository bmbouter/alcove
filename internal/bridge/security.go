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
	"sort"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// SecurityProfile is a named, reusable bundle of tool + repo + operation permissions.
type SecurityProfile struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name" yaml:"name"`
	DisplayName string                       `json:"display_name,omitempty" yaml:"display_name"`
	Description string                       `json:"description,omitempty" yaml:"description"`
	Tools       map[string]ProfileToolConfig `json:"tools" yaml:"tools"`
	TeamID      string                       `json:"team_id,omitempty"`
	IsBuiltin   bool                         `json:"is_builtin"`
	Source      string                       `json:"source"`
	SourceRepo  string                       `json:"source_repo,omitempty"`
	SourceKey   string                       `json:"source_key,omitempty"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

// ProfileToolRule specifies a single repo+operations rule within a tool config.
type ProfileToolRule struct {
	Repos      []string `json:"repos" yaml:"repos"`
	Operations []string `json:"operations" yaml:"operations"`
}

// ProfileToolConfig specifies per-tool configuration within a security profile.
// Supports two formats:
//   - Legacy flat: {"operations": [...], "repos": [...]}
//   - Multi-rule:  {"rules": [{"repos": [...], "operations": [...]}, ...]}
//
// When Rules is non-empty, it takes precedence over the flat Operations/Repos fields.
type ProfileToolConfig struct {
	Operations []string          `json:"operations,omitempty" yaml:"operations"`
	Repos      []string          `json:"repos,omitempty" yaml:"repos"`
	Rules      []ProfileToolRule `json:"rules,omitempty" yaml:"rules"`
}

// FlattenRules returns a single (operations, repos) pair by unioning all rules.
// If the config uses the legacy flat format, it returns Operations and Repos directly.
func (c ProfileToolConfig) FlattenRules() (operations []string, repos []string) {
	if len(c.Rules) == 0 {
		return c.Operations, c.Repos
	}

	opSet := make(map[string]bool)
	repoSet := make(map[string]bool)
	wildcard := false

	for _, rule := range c.Rules {
		for _, op := range rule.Operations {
			opSet[op] = true
		}
		for _, r := range rule.Repos {
			if r == "*" {
				wildcard = true
			}
			repoSet[r] = true
		}
	}

	operations = make([]string, 0, len(opSet))
	for op := range opSet {
		operations = append(operations, op)
	}
	sort.Strings(operations)

	if wildcard {
		repos = []string{"*"}
	} else {
		repos = make([]string, 0, len(repoSet))
		for r := range repoSet {
			repos = append(repos, r)
		}
		sort.Strings(repos)
	}

	return operations, repos
}

// ProfileStore manages security profiles in the database.
type ProfileStore struct {
	db *pgxpool.Pool
}

// NewProfileStore creates a ProfileStore with the given database pool.
func NewProfileStore(db *pgxpool.Pool) *ProfileStore {
	return &ProfileStore{db: db}
}

// CreateProfile inserts a new security profile for the given team.
func (ps *ProfileStore) CreateProfile(ctx context.Context, profile *SecurityProfile, teamID string) error {
	if profile.ID == "" {
		profile.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	profile.TeamID = teamID
	profile.IsBuiltin = false
	profile.CreatedAt = now
	profile.UpdatedAt = now

	toolsJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}

	_, err = ps.db.Exec(ctx,
		`INSERT INTO security_profiles (id, name, display_name, description, tools, team_id, is_builtin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		profile.ID, profile.Name, profile.DisplayName, profile.Description,
		string(toolsJSON), profile.TeamID, profile.IsBuiltin,
		profile.CreatedAt, profile.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting profile: %w", err)
	}
	return nil
}

// ListProfiles returns the given team's profiles.
func (ps *ProfileStore) ListProfiles(ctx context.Context, teamID string) ([]SecurityProfile, error) {
	query := `SELECT id, name, display_name, description, tools, team_id, is_builtin, source, source_repo, source_key, created_at, updated_at
		FROM security_profiles
		WHERE team_id = $1
		ORDER BY name ASC`

	rows, err := ps.db.Query(ctx, query, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying profiles: %w", err)
	}
	defer rows.Close()

	var profiles []SecurityProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, *p)
	}

	if profiles == nil {
		profiles = []SecurityProfile{}
	}

	return profiles, rows.Err()
}

// GetProfile looks up a profile by name, scoped to the given team.
func (ps *ProfileStore) GetProfile(ctx context.Context, name, teamID string) (*SecurityProfile, error) {
	query := `SELECT id, name, display_name, description, tools, team_id, is_builtin, source, source_repo, source_key, created_at, updated_at
		FROM security_profiles
		WHERE name = $1 AND team_id = $2
		ORDER BY source ASC
		LIMIT 1`

	row := ps.db.QueryRow(ctx, query, name, teamID)
	return scanProfileRow(row)
}

// UpdateProfile updates an existing profile. YAML-sourced profiles cannot be updated.
func (ps *ProfileStore) UpdateProfile(ctx context.Context, profile *SecurityProfile, teamID string) error {
	toolsJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}

	now := time.Now().UTC()
	result, err := ps.db.Exec(ctx,
		`UPDATE security_profiles
		SET display_name = $1, description = $2, tools = $3, updated_at = $4
		WHERE name = $5 AND team_id = $6 AND source != 'yaml'`,
		profile.DisplayName, profile.Description, string(toolsJSON), now,
		profile.Name, teamID)
	if err != nil {
		return fmt.Errorf("updating profile: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile %q not found or is a YAML-sourced profile", profile.Name)
	}
	profile.UpdatedAt = now
	return nil
}

// DeleteProfile removes a profile. YAML-sourced profiles cannot be deleted.
func (ps *ProfileStore) DeleteProfile(ctx context.Context, name, teamID string) error {
	result, err := ps.db.Exec(ctx,
		`DELETE FROM security_profiles WHERE name = $1 AND team_id = $2 AND source != 'yaml'`,
		name, teamID)
	if err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile %q not found or is a YAML-sourced profile", name)
	}
	return nil
}

// MergeProfiles takes multiple profile names, looks them up, and returns a merged Scope
// and merged tool config map. Union merge: operations unioned, repos unioned, wildcard wins.
func (ps *ProfileStore) MergeProfiles(ctx context.Context, names []string, teamID string) (internal.Scope, map[string]ProfileToolConfig, error) {
	scope := internal.Scope{Services: make(map[string]internal.ServiceScope)}
	merged := make(map[string]ProfileToolConfig)

	for _, name := range names {
		profile, err := ps.GetProfile(ctx, name, teamID)
		if err != nil {
			return scope, nil, fmt.Errorf("profile %q not found: %w", name, err)
		}

		for tool, cfg := range profile.Tools {
			existing := merged[tool]

			// Flatten rules from both existing and incoming configs.
			existOps, existRepos := existing.FlattenRules()
			cfgOps, cfgRepos := cfg.FlattenRules()

			// Union operations.
			opSet := make(map[string]bool)
			for _, op := range existOps {
				opSet[op] = true
			}
			for _, op := range cfgOps {
				opSet[op] = true
			}
			ops := make([]string, 0, len(opSet))
			for op := range opSet {
				ops = append(ops, op)
			}
			sort.Strings(ops)

			// Union repos (wildcard wins).
			var repos []string
			if containsStr(existRepos, "*") || containsStr(cfgRepos, "*") {
				repos = []string{"*"}
			} else {
				repoSet := make(map[string]bool)
				for _, r := range existRepos {
					repoSet[r] = true
				}
				for _, r := range cfgRepos {
					repoSet[r] = true
				}
				repos = make([]string, 0, len(repoSet))
				for r := range repoSet {
					repos = append(repos, r)
				}
				sort.Strings(repos)
			}

			merged[tool] = ProfileToolConfig{Operations: ops, Repos: repos}
			scope.Services[tool] = internal.ServiceScope{Operations: ops, Repos: repos}
		}
	}

	return scope, merged, nil
}

// scanProfile scans a SecurityProfile from a rows result.
func scanProfile(rows interface{ Scan(dest ...any) error }) (*SecurityProfile, error) {
	var p SecurityProfile
	var toolsJSON string

	if err := rows.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Description,
		&toolsJSON, &p.TeamID, &p.IsBuiltin, &p.Source, &p.SourceRepo, &p.SourceKey,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, fmt.Errorf("scanning profile: %w", err)
	}

	if err := json.Unmarshal([]byte(toolsJSON), &p.Tools); err != nil {
		return nil, fmt.Errorf("unmarshaling profile tools: %w", err)
	}

	return &p, nil
}

// scanProfileRow scans a SecurityProfile from a single row result.
func scanProfileRow(row interface{ Scan(dest ...any) error }) (*SecurityProfile, error) {
	return scanProfile(row)
}

// ParseSecurityProfile parses a YAML security profile definition.
func ParseSecurityProfile(data []byte) (*SecurityProfile, error) {
	var p SecurityProfile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("security profile missing required field: name")
	}
	if len(p.Tools) == 0 {
		return nil, fmt.Errorf("security profile %q missing required field: tools", p.Name)
	}
	return &p, nil
}

// UpsertYAMLProfile inserts or updates a YAML-sourced security profile.
func (ps *ProfileStore) UpsertYAMLProfile(ctx context.Context, profile *SecurityProfile) error {
	if profile.ID == "" {
		profile.ID = uuid.New().String()
	}
	toolsJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}

	_, err = ps.db.Exec(ctx,
		`INSERT INTO security_profiles (id, name, display_name, description, tools, team_id, is_builtin, source, source_repo, source_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, false, 'yaml', $7, $8, NOW(), NOW())
		ON CONFLICT (source_key) WHERE source_key != '' DO UPDATE SET
			name = EXCLUDED.name,
			display_name = EXCLUDED.display_name,
			description = EXCLUDED.description,
			tools = EXCLUDED.tools,
			team_id = EXCLUDED.team_id,
			source_repo = EXCLUDED.source_repo,
			updated_at = NOW()`,
		profile.ID, profile.Name, profile.DisplayName, profile.Description,
		string(toolsJSON), profile.TeamID, profile.SourceRepo, profile.SourceKey)
	if err != nil {
		return fmt.Errorf("upserting YAML profile: %w", err)
	}
	return nil
}

// DeleteYAMLProfilesByRepo removes all YAML-sourced profiles from the given repo and team.
func (ps *ProfileStore) DeleteYAMLProfilesByRepo(ctx context.Context, repoURL, teamID string) error {
	_, err := ps.db.Exec(ctx,
		`DELETE FROM security_profiles WHERE source = 'yaml' AND source_repo = $1 AND team_id = $2`, repoURL, teamID)
	return err
}

// ListYAMLProfileKeysByRepo returns source_keys for all YAML profiles from the given repo and team.
func (ps *ProfileStore) ListYAMLProfileKeysByRepo(ctx context.Context, repoURL, teamID string) ([]string, error) {
	rows, err := ps.db.Query(ctx,
		`SELECT source_key FROM security_profiles WHERE source = 'yaml' AND source_repo = $1 AND team_id = $2`, repoURL, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// containsStr checks if a string slice contains a given string.
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
