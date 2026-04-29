package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alcove-ai/alcove/internal"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

// RepoGroupDefinition represents a named group of repositories defined in YAML.
type RepoGroupDefinition struct {
	ID          string              `json:"id"`
	Name        string              `json:"name" yaml:"name"`
	Description string              `json:"description,omitempty" yaml:"description"`
	Repos       []internal.RepoSpec `json:"repos" yaml:"repos"`

	SourceRepo string    `json:"source_repo,omitempty"`
	SourceFile string    `json:"source_file,omitempty"`
	SourceKey  string    `json:"source_key,omitempty"`
	RawYAML    string    `json:"raw_yaml,omitempty"`
	SyncError  string    `json:"sync_error,omitempty"`
	LastSynced time.Time `json:"last_synced,omitempty"`
	TeamID     string    `json:"team_id,omitempty"`
}

// ParseRepoGroupDefinition parses a YAML byte slice into a RepoGroupDefinition.
func ParseRepoGroupDefinition(data []byte) (*RepoGroupDefinition, error) {
	var rg RepoGroupDefinition
	if err := yaml.Unmarshal(data, &rg); err != nil {
		return nil, fmt.Errorf("YAML syntax error: %w", err)
	}

	if rg.Name == "" {
		return nil, fmt.Errorf("repo group missing required field: name")
	}
	if len(rg.Repos) == 0 {
		return nil, fmt.Errorf("repo group %q has no repos", rg.Name)
	}

	namesSeen := make(map[string]bool)
	for i := range rg.Repos {
		if rg.Repos[i].URL == "" {
			return nil, fmt.Errorf("repo group %q: repos[%d] has empty URL", rg.Name, i)
		}
		if rg.Repos[i].Name == "" {
			rg.Repos[i].Name = repoNameFromURL(rg.Repos[i].URL)
		}
		if namesSeen[rg.Repos[i].Name] {
			return nil, fmt.Errorf("repo group %q: duplicate repo name %q", rg.Name, rg.Repos[i].Name)
		}
		namesSeen[rg.Repos[i].Name] = true
	}

	return &rg, nil
}

// RepoGroupStore manages repo group definitions in PostgreSQL.
type RepoGroupStore struct {
	db *pgxpool.Pool
}

// NewRepoGroupStore creates a RepoGroupStore.
func NewRepoGroupStore(db *pgxpool.Pool) *RepoGroupStore {
	return &RepoGroupStore{db: db}
}

// UpsertRepoGroup inserts or updates a repo group definition by source_key.
func (s *RepoGroupStore) UpsertRepoGroup(ctx context.Context, rg *RepoGroupDefinition, sourceKey, rawYAML, syncError string) error {
	id := uuid.New().String()
	parsedJSON, err := json.Marshal(rg)
	if err != nil {
		return fmt.Errorf("marshaling repo group: %w", err)
	}
	now := time.Now().UTC()

	_, err = s.db.Exec(ctx, `
		INSERT INTO repo_groups (id, name, description, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, team_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (source_key, team_id) DO UPDATE SET
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			source_repo = EXCLUDED.source_repo,
			source_file = EXCLUDED.source_file,
			raw_yaml = EXCLUDED.raw_yaml,
			parsed = EXCLUDED.parsed,
			sync_error = EXCLUDED.sync_error,
			last_synced = EXCLUDED.last_synced,
			updated_at = EXCLUDED.updated_at
	`, id, rg.Name, rg.Description, rg.SourceRepo, rg.SourceFile, sourceKey, rawYAML, parsedJSON, nilIfEmpty(syncError), now, rg.TeamID, now, now)
	if err != nil {
		return fmt.Errorf("upserting repo group: %w", err)
	}
	return nil
}

// ListRepoGroups returns all repo groups for a team.
func (s *RepoGroupStore) ListRepoGroups(ctx context.Context, teamID string) ([]RepoGroupDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, team_id
		FROM repo_groups
		WHERE team_id = $1
		ORDER BY name ASC
	`, teamID)
	if err != nil {
		return nil, fmt.Errorf("listing repo groups: %w", err)
	}
	defer rows.Close()

	var groups []RepoGroupDefinition
	for rows.Next() {
		var rg RepoGroupDefinition
		var parsedJSON []byte
		var syncError *string
		var lastSynced *time.Time
		if err := rows.Scan(&rg.ID, &rg.Name, &rg.Description, &rg.SourceRepo, &rg.SourceFile, &rg.SourceKey, &rg.RawYAML, &parsedJSON, &syncError, &lastSynced, &rg.TeamID); err != nil {
			return nil, fmt.Errorf("scanning repo group: %w", err)
		}
		if syncError != nil {
			rg.SyncError = *syncError
		}
		if lastSynced != nil {
			rg.LastSynced = *lastSynced
		}
		if parsedJSON != nil {
			var parsed RepoGroupDefinition
			if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
				rg.Repos = parsed.Repos
			}
		}
		groups = append(groups, rg)
	}
	return groups, nil
}

// GetRepoGroup returns a repo group by name or ID for a team.
func (s *RepoGroupStore) GetRepoGroup(ctx context.Context, nameOrID, teamID string) (*RepoGroupDefinition, error) {
	var rg RepoGroupDefinition
	var parsedJSON []byte
	var syncError *string
	var lastSynced *time.Time

	err := s.db.QueryRow(ctx, `
		SELECT id, name, description, source_repo, source_file, source_key, raw_yaml, parsed, sync_error, last_synced, team_id
		FROM repo_groups
		WHERE (id = $1 OR name = $1) AND team_id = $2
	`, nameOrID, teamID).Scan(&rg.ID, &rg.Name, &rg.Description, &rg.SourceRepo, &rg.SourceFile, &rg.SourceKey, &rg.RawYAML, &parsedJSON, &syncError, &lastSynced, &rg.TeamID)
	if err != nil {
		return nil, fmt.Errorf("repo group %q not found: %w", nameOrID, err)
	}
	if syncError != nil {
		rg.SyncError = *syncError
	}
	if lastSynced != nil {
		rg.LastSynced = *lastSynced
	}
	if parsedJSON != nil {
		var parsed RepoGroupDefinition
		if err := json.Unmarshal(parsedJSON, &parsed); err == nil {
			rg.Repos = parsed.Repos
		}
	}
	return &rg, nil
}

// ListRepoGroupsByRepo returns all repo groups from a given source repo.
func (s *RepoGroupStore) ListRepoGroupsByRepo(ctx context.Context, repoURL, teamID string) ([]RepoGroupDefinition, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, source_key
		FROM repo_groups
		WHERE source_repo = $1 AND team_id = $2
	`, repoURL, teamID)
	if err != nil {
		return nil, fmt.Errorf("listing repo groups by repo: %w", err)
	}
	defer rows.Close()

	var groups []RepoGroupDefinition
	for rows.Next() {
		var rg RepoGroupDefinition
		if err := rows.Scan(&rg.ID, &rg.Name, &rg.SourceKey); err != nil {
			return nil, fmt.Errorf("scanning repo group: %w", err)
		}
		groups = append(groups, rg)
	}
	return groups, nil
}

// DeleteRepoGroupsByRepo removes all repo groups from a given source repo.
func (s *RepoGroupStore) DeleteRepoGroupsByRepo(ctx context.Context, repoURL, teamID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM repo_groups WHERE source_repo = $1 AND team_id = $2`, repoURL, teamID)
	return err
}

// ListDistinctSourceRepos returns all distinct source_repo values for repo groups belonging to the given team.
func (s *RepoGroupStore) ListDistinctSourceRepos(ctx context.Context, teamID string) ([]string, error) {
	query := `
		SELECT DISTINCT source_repo
		FROM repo_groups
		WHERE team_id = $1 AND source_repo != ''
		ORDER BY source_repo
	`

	rows, err := s.db.Query(ctx, query, teamID)
	if err != nil {
		return nil, fmt.Errorf("querying distinct source repos: %w", err)
	}
	defer rows.Close()

	var repos []string
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("scanning source repo: %w", err)
		}
		repos = append(repos, repo)
	}

	return repos, rows.Err()
}
