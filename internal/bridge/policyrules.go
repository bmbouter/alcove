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

	"github.com/bmbouter/alcove/internal"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// NamedRuleSet is a named collection of HTTP policy rules that can be
// referenced by security profiles.
type NamedRuleSet struct {
	Name  string                `json:"name" yaml:"name"`
	Rules []internal.PolicyRule `json:"rules" yaml:"rules"`
}

// PolicyRuleFile is the top-level structure of a .alcove/policy-rules/*.yml file.
type PolicyRuleFile struct {
	RuleSets []NamedRuleSet `json:"rule_sets" yaml:"rule_sets"`
}

// ProfileRuleEntry represents a single entry in a security profile's rules list.
// It can be either a string reference to a named rule set, or an inline rule object.
type ProfileRuleEntry struct {
	Ref   string             // named rule set reference (when YAML entry is a string)
	Allow *internal.HTTPRule // inline rule (when YAML entry is a map with "allow")
}

// UnmarshalYAML implements custom YAML unmarshaling for ProfileRuleEntry.
// A string value becomes a Ref; an object with "allow" becomes an inline rule.
func (e *ProfileRuleEntry) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		e.Ref = value.Value
		return nil
	}
	if value.Kind == yaml.MappingNode {
		var rule internal.PolicyRule
		if err := value.Decode(&rule); err != nil {
			return fmt.Errorf("decoding inline policy rule: %w", err)
		}
		e.Allow = &rule.Allow
		return nil
	}
	return fmt.Errorf("policy rule entry must be a string or an object, got %v", value.Kind)
}

// MarshalJSON implements custom JSON marshaling for ProfileRuleEntry.
func (e ProfileRuleEntry) MarshalJSON() ([]byte, error) {
	if e.Ref != "" {
		return json.Marshal(e.Ref)
	}
	if e.Allow != nil {
		return json.Marshal(internal.PolicyRule{Allow: *e.Allow})
	}
	return []byte("null"), nil
}

// UnmarshalJSON implements custom JSON unmarshaling for ProfileRuleEntry.
func (e *ProfileRuleEntry) UnmarshalJSON(data []byte) error {
	// Try string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Ref = s
		return nil
	}
	// Try inline rule object.
	var rule internal.PolicyRule
	if err := json.Unmarshal(data, &rule); err == nil && rule.Allow.Host != "" {
		e.Allow = &rule.Allow
		return nil
	}
	return fmt.Errorf("policy rule entry must be a string or an object with 'allow'")
}

// PolicyRuleStore manages named policy rule sets in the database.
type PolicyRuleStore struct {
	db *pgxpool.Pool
}

// NewPolicyRuleStore creates a PolicyRuleStore with the given database pool.
func NewPolicyRuleStore(db *pgxpool.Pool) *PolicyRuleStore {
	return &PolicyRuleStore{db: db}
}

// UpsertRuleSet inserts or updates a named rule set, keyed by source_key.
func (s *PolicyRuleStore) UpsertRuleSet(ctx context.Context, name, rulesJSON, teamID, sourceRepo, sourceFile, sourceKey string) error {
	id := uuid.New().String()
	_, err := s.db.Exec(ctx,
		`INSERT INTO policy_rule_sets (id, name, rules, team_id, source_repo, source_file, source_key, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, NOW(), NOW())
		ON CONFLICT (source_key) WHERE source_key IS NOT NULL DO UPDATE SET
			name = EXCLUDED.name,
			rules = EXCLUDED.rules,
			team_id = EXCLUDED.team_id,
			source_repo = EXCLUDED.source_repo,
			source_file = EXCLUDED.source_file,
			updated_at = NOW()`,
		id, name, rulesJSON, teamID, sourceRepo, sourceFile, sourceKey)
	if err != nil {
		return fmt.Errorf("upserting policy rule set %q: %w", name, err)
	}
	return nil
}

// GetRuleSet looks up a named rule set by name and team.
func (s *PolicyRuleStore) GetRuleSet(ctx context.Context, name, teamID string) (*NamedRuleSet, error) {
	var rulesJSON string
	var rsName string
	err := s.db.QueryRow(ctx,
		`SELECT name, rules FROM policy_rule_sets WHERE name = $1 AND team_id = $2`,
		name, teamID).Scan(&rsName, &rulesJSON)
	if err != nil {
		return nil, fmt.Errorf("rule set %q not found: %w", name, err)
	}

	var rules []internal.PolicyRule
	if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
		return nil, fmt.Errorf("unmarshaling rules for %q: %w", name, err)
	}

	return &NamedRuleSet{Name: rsName, Rules: rules}, nil
}

// ListRuleSets returns all named rule sets for a team.
func (s *PolicyRuleStore) ListRuleSets(ctx context.Context, teamID string) ([]NamedRuleSet, error) {
	rows, err := s.db.Query(ctx,
		`SELECT name, rules FROM policy_rule_sets WHERE team_id = $1 ORDER BY name ASC`, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying rule sets: %w", err)
	}
	defer rows.Close()

	var result []NamedRuleSet
	for rows.Next() {
		var name, rulesJSON string
		if err := rows.Scan(&name, &rulesJSON); err != nil {
			return nil, fmt.Errorf("scanning rule set: %w", err)
		}
		var rules []internal.PolicyRule
		if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
			return nil, fmt.Errorf("unmarshaling rules for %q: %w", name, err)
		}
		result = append(result, NamedRuleSet{Name: name, Rules: rules})
	}
	if result == nil {
		result = []NamedRuleSet{}
	}
	return result, rows.Err()
}

// DeleteByRepo removes all rule sets from a given source repo and team.
func (s *PolicyRuleStore) DeleteByRepo(ctx context.Context, sourceRepo, teamID string) error {
	_, err := s.db.Exec(ctx,
		`DELETE FROM policy_rule_sets WHERE source_repo = $1 AND team_id = $2`,
		sourceRepo, teamID)
	return err
}

// ListSourceKeysByRepo returns source_keys for all rule sets from a given repo and team.
func (s *PolicyRuleStore) ListSourceKeysByRepo(ctx context.Context, sourceRepo, teamID string) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT source_key FROM policy_rule_sets WHERE source_repo = $1 AND team_id = $2`, sourceRepo, teamID)
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

// StoredRuleSet represents a rule set as stored in the database, including metadata.
type StoredRuleSet struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Rules      string    `json:"rules"` // raw JSON
	TeamID     string    `json:"team_id"`
	SourceRepo string    `json:"source_repo"`
	SourceFile string    `json:"source_file"`
	SourceKey  string    `json:"source_key"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ResolvePolicyRules expands profile rule entries (named references and inline rules)
// into a flat list of PolicyRule values. For each entry: if it's a string reference,
// look up the named rule set and append its rules. If it's an inline rule, append directly.
func ResolvePolicyRules(ctx context.Context, store *PolicyRuleStore, profileRules []ProfileRuleEntry, teamID string) ([]internal.PolicyRule, error) {
	var result []internal.PolicyRule
	for _, entry := range profileRules {
		if entry.Ref != "" {
			rs, err := store.GetRuleSet(ctx, entry.Ref, teamID)
			if err != nil {
				return nil, fmt.Errorf("resolving rule set %q: %w", entry.Ref, err)
			}
			result = append(result, rs.Rules...)
		} else if entry.Allow != nil {
			result = append(result, internal.PolicyRule{Allow: *entry.Allow})
		}
	}
	return result, nil
}
