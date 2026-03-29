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
)

// SecurityProfile is a named, reusable bundle of tool + repo + operation permissions.
type SecurityProfile struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	DisplayName string                       `json:"display_name,omitempty"`
	Description string                       `json:"description,omitempty"`
	Tools       map[string]ProfileToolConfig `json:"tools"`
	Owner       string                       `json:"owner,omitempty"`
	IsBuiltin   bool                         `json:"is_builtin"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

// ProfileToolRule specifies a single repo+operations rule within a tool config.
type ProfileToolRule struct {
	Repos      []string `json:"repos"`
	Operations []string `json:"operations"`
}

// ProfileToolConfig specifies per-tool configuration within a security profile.
// Supports two formats:
//   - Legacy flat: {"operations": [...], "repos": [...]}
//   - Multi-rule:  {"rules": [{"repos": [...], "operations": [...]}, ...]}
//
// When Rules is non-empty, it takes precedence over the flat Operations/Repos fields.
type ProfileToolConfig struct {
	Operations []string          `json:"operations,omitempty"`
	Repos      []string          `json:"repos,omitempty"`
	Rules      []ProfileToolRule `json:"rules,omitempty"`
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

// CreateProfile inserts a new security profile owned by the given owner.
func (ps *ProfileStore) CreateProfile(ctx context.Context, profile *SecurityProfile, owner string) error {
	if profile.ID == "" {
		profile.ID = uuid.New().String()
	}
	now := time.Now().UTC()
	profile.Owner = owner
	profile.IsBuiltin = false
	profile.CreatedAt = now
	profile.UpdatedAt = now

	toolsJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}

	_, err = ps.db.Exec(ctx,
		`INSERT INTO security_profiles (id, name, display_name, description, tools, owner, is_builtin, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		profile.ID, profile.Name, profile.DisplayName, profile.Description,
		string(toolsJSON), profile.Owner, profile.IsBuiltin,
		profile.CreatedAt, profile.UpdatedAt)
	if err != nil {
		return fmt.Errorf("inserting profile: %w", err)
	}
	return nil
}

// ListProfiles returns ALL builtin profiles plus the given owner's custom profiles.
func (ps *ProfileStore) ListProfiles(ctx context.Context, owner string) ([]SecurityProfile, error) {
	query := `SELECT id, name, display_name, description, tools, owner, is_builtin, created_at, updated_at
		FROM security_profiles
		WHERE owner = $1 OR is_builtin = true
		ORDER BY is_builtin DESC, name ASC`

	rows, err := ps.db.Query(ctx, query, owner)
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

// GetProfile looks up a profile by name. User profiles take priority over builtins.
func (ps *ProfileStore) GetProfile(ctx context.Context, name, owner string) (*SecurityProfile, error) {
	// Try user's own profile first, then fall back to builtin.
	query := `SELECT id, name, display_name, description, tools, owner, is_builtin, created_at, updated_at
		FROM security_profiles
		WHERE name = $1 AND (owner = $2 OR is_builtin = true)
		ORDER BY is_builtin ASC
		LIMIT 1`

	row := ps.db.QueryRow(ctx, query, name, owner)
	return scanProfileRow(row)
}

// UpdateProfile updates an existing profile. Builtin profiles cannot be updated.
func (ps *ProfileStore) UpdateProfile(ctx context.Context, profile *SecurityProfile, owner string) error {
	toolsJSON, err := json.Marshal(profile.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}

	now := time.Now().UTC()
	result, err := ps.db.Exec(ctx,
		`UPDATE security_profiles
		SET display_name = $1, description = $2, tools = $3, updated_at = $4
		WHERE name = $5 AND owner = $6 AND is_builtin = false`,
		profile.DisplayName, profile.Description, string(toolsJSON), now,
		profile.Name, owner)
	if err != nil {
		return fmt.Errorf("updating profile: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile %q not found or is a builtin profile", profile.Name)
	}
	profile.UpdatedAt = now
	return nil
}

// DeleteProfile removes a profile. Builtin profiles cannot be deleted.
func (ps *ProfileStore) DeleteProfile(ctx context.Context, name, owner string) error {
	result, err := ps.db.Exec(ctx,
		`DELETE FROM security_profiles WHERE name = $1 AND owner = $2 AND is_builtin = false`,
		name, owner)
	if err != nil {
		return fmt.Errorf("deleting profile: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("profile %q not found or is a builtin profile", name)
	}
	return nil
}

// MergeProfiles takes multiple profile names, looks them up, and returns a merged Scope
// and merged tool config map. Union merge: operations unioned, repos unioned, wildcard wins.
func (ps *ProfileStore) MergeProfiles(ctx context.Context, names []string, owner string) (internal.Scope, map[string]ProfileToolConfig, error) {
	scope := internal.Scope{Services: make(map[string]internal.ServiceScope)}
	merged := make(map[string]ProfileToolConfig)

	for _, name := range names {
		profile, err := ps.GetProfile(ctx, name, owner)
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

// SeedBuiltinProfiles creates or updates the starter security profiles.
// Uses INSERT ... ON CONFLICT DO UPDATE for idempotent seeding.
func (ps *ProfileStore) SeedBuiltinProfiles(ctx context.Context) error {
	builtins := []SecurityProfile{
		{
			Name:        "read-only",
			DisplayName: "Read Only",
			Description: "Read-only access to all repositories on all configured services",
			Tools: map[string]ProfileToolConfig{
				"github": {
					Repos: []string{"*"},
					Operations: []string{
						"clone", "read_prs", "read_issues", "read_contents", "read_actions",
					},
				},
				"gitlab": {
					Repos: []string{"*"},
					Operations: []string{
						"clone", "read_mrs", "read_issues", "read_contents", "read_pipelines",
					},
				},
			},
		},
		{
			Name:        "contributor",
			DisplayName: "Contributor",
			Description: "Read access plus branch push and draft PR/MR creation on all repos",
			Tools: map[string]ProfileToolConfig{
				"github": {
					Repos: []string{"*"},
					Operations: []string{
						"clone", "read_prs", "read_issues", "read_contents", "read_actions",
						"push_branch", "create_pr_draft", "create_comment",
					},
				},
				"gitlab": {
					Repos: []string{"*"},
					Operations: []string{
						"clone", "read_mrs", "read_issues", "read_contents", "read_pipelines",
						"push_branch", "create_mr_draft", "create_comment",
					},
				},
			},
		},
		{
			Name:        "maintainer",
			DisplayName: "Maintainer",
			Description: "Full access including PR/MR merge and branch deletion on all repos",
			Tools: map[string]ProfileToolConfig{
				"github": {
					Repos:      []string{"*"},
					Operations: []string{"*"},
				},
				"gitlab": {
					Repos:      []string{"*"},
					Operations: []string{"*"},
				},
			},
		},
	}

	for _, b := range builtins {
		id := uuid.New().String()
		toolsJSON, err := json.Marshal(b.Tools)
		if err != nil {
			return fmt.Errorf("marshaling tools for profile %q: %w", b.Name, err)
		}

		_, err = ps.db.Exec(ctx,
			`INSERT INTO security_profiles (id, name, display_name, description, tools, owner, is_builtin, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, '', true, NOW(), NOW())
			ON CONFLICT (name, owner) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				description = EXCLUDED.description,
				tools = EXCLUDED.tools,
				is_builtin = EXCLUDED.is_builtin,
				updated_at = NOW()`,
			id, b.Name, b.DisplayName, b.Description, string(toolsJSON))
		if err != nil {
			return fmt.Errorf("seeding builtin profile %q: %w", b.Name, err)
		}
	}

	return nil
}

// scanProfile scans a SecurityProfile from a rows result.
func scanProfile(rows interface{ Scan(dest ...any) error }) (*SecurityProfile, error) {
	var p SecurityProfile
	var toolsJSON string

	if err := rows.Scan(&p.ID, &p.Name, &p.DisplayName, &p.Description,
		&toolsJSON, &p.Owner, &p.IsBuiltin, &p.CreatedAt, &p.UpdatedAt); err != nil {
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

// containsStr checks if a string slice contains a given string.
func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
