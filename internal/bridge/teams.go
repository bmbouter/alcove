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
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Team represents a team that owns shared resources.
type Team struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	IsPersonal bool      `json:"is_personal"`
	CreatedAt  time.Time `json:"created_at"`
	Members    []string  `json:"members,omitempty"`
}

// TeamStore manages teams in PostgreSQL.
type TeamStore struct {
	db *pgxpool.Pool
}

// NewTeamStore creates a TeamStore with the given database pool.
func NewTeamStore(db *pgxpool.Pool) *TeamStore {
	return &TeamStore{db: db}
}

// ListTeamsForUser returns all teams the user is a member of.
func (ts *TeamStore) ListTeamsForUser(ctx context.Context, username string) ([]Team, error) {
	rows, err := ts.db.Query(ctx, `
		SELECT t.id, t.name, t.is_personal, t.created_at
		FROM teams t
		JOIN team_members tm ON t.id = tm.team_id
		WHERE tm.username = $1
		ORDER BY t.is_personal DESC, t.name ASC
	`, username)
	if err != nil {
		return nil, fmt.Errorf("querying teams: %w", err)
	}
	defer rows.Close()

	var teams []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.IsPersonal, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning team: %w", err)
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating teams: %w", err)
	}

	if teams == nil {
		teams = []Team{}
	}
	return teams, nil
}

// GetTeam retrieves a team by ID including its members.
func (ts *TeamStore) GetTeam(ctx context.Context, id string) (*Team, error) {
	var t Team
	err := ts.db.QueryRow(ctx, `
		SELECT id, name, is_personal, created_at FROM teams WHERE id = $1
	`, id).Scan(&t.ID, &t.Name, &t.IsPersonal, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("team not found: %w", err)
	}

	// Fetch members.
	rows, err := ts.db.Query(ctx, `SELECT username FROM team_members WHERE team_id = $1 ORDER BY username`, id)
	if err != nil {
		return nil, fmt.Errorf("querying team members: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var username string
		if err := rows.Scan(&username); err != nil {
			return nil, fmt.Errorf("scanning team member: %w", err)
		}
		t.Members = append(t.Members, username)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating team members: %w", err)
	}
	if t.Members == nil {
		t.Members = []string{}
	}

	return &t, nil
}

// CreateTeam creates a new team and adds the creator as a member.
func (ts *TeamStore) CreateTeam(ctx context.Context, name, creator string) (*Team, error) {
	id := uuid.New().String()
	now := time.Now().UTC()

	tx, err := ts.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `INSERT INTO teams (id, name, is_personal, created_at) VALUES ($1, $2, false, $3)`,
		id, name, now)
	if err != nil {
		return nil, fmt.Errorf("inserting team: %w", err)
	}

	_, err = tx.Exec(ctx, `INSERT INTO team_members (team_id, username) VALUES ($1, $2)`, id, creator)
	if err != nil {
		return nil, fmt.Errorf("adding creator as member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	return &Team{
		ID:         id,
		Name:       name,
		IsPersonal: false,
		CreatedAt:  now,
		Members:    []string{creator},
	}, nil
}

// RenameTeam renames a team. Cannot rename personal teams.
func (ts *TeamStore) RenameTeam(ctx context.Context, id, newName string) error {
	result, err := ts.db.Exec(ctx,
		`UPDATE teams SET name = $1 WHERE id = $2 AND is_personal = false`, newName, id)
	if err != nil {
		return fmt.Errorf("renaming team: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("team not found or is a personal team")
	}
	return nil
}

// DeleteTeam removes a team. Cannot delete personal teams.
func (ts *TeamStore) DeleteTeam(ctx context.Context, id string) error {
	result, err := ts.db.Exec(ctx, `DELETE FROM teams WHERE id = $1 AND is_personal = false`, id)
	if err != nil {
		return fmt.Errorf("deleting team: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("team not found or is a personal team")
	}
	return nil
}

// AddMember adds a user to a team. Cannot add members to personal teams.
// The username must correspond to an existing user in auth_users.
func (ts *TeamStore) AddMember(ctx context.Context, teamID, username string) error {
	// Verify team is not personal.
	var isPersonal bool
	err := ts.db.QueryRow(ctx, `SELECT is_personal FROM teams WHERE id = $1`, teamID).Scan(&isPersonal)
	if err != nil {
		return fmt.Errorf("team not found: %w", err)
	}
	if isPersonal {
		return fmt.Errorf("cannot add members to a personal team")
	}

	// Verify user exists.
	var exists bool
	err = ts.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_users WHERE username = $1)`, username).Scan(&exists)
	if err != nil {
		return fmt.Errorf("checking user existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("user %q does not exist", username)
	}

	_, err = ts.db.Exec(ctx,
		`INSERT INTO team_members (team_id, username) VALUES ($1, $2) ON CONFLICT DO NOTHING`, teamID, username)
	if err != nil {
		return fmt.Errorf("adding member: %w", err)
	}
	return nil
}

// RemoveMember removes a user from a team. Cannot remove from personal teams.
func (ts *TeamStore) RemoveMember(ctx context.Context, teamID, username string) error {
	// Verify team is not personal.
	var isPersonal bool
	err := ts.db.QueryRow(ctx, `SELECT is_personal FROM teams WHERE id = $1`, teamID).Scan(&isPersonal)
	if err != nil {
		return fmt.Errorf("team not found: %w", err)
	}
	if isPersonal {
		return fmt.Errorf("cannot remove members from a personal team")
	}

	result, err := ts.db.Exec(ctx,
		`DELETE FROM team_members WHERE team_id = $1 AND username = $2`, teamID, username)
	if err != nil {
		return fmt.Errorf("removing member: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("member not found")
	}
	return nil
}

// IsMember checks if a user is a member of a team.
func (ts *TeamStore) IsMember(ctx context.Context, teamID, username string) (bool, error) {
	var exists bool
	err := ts.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_members WHERE team_id = $1 AND username = $2)`,
		teamID, username).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking membership: %w", err)
	}
	return exists, nil
}

// GetPersonalTeamID returns the personal team ID for a user.
func (ts *TeamStore) GetPersonalTeamID(ctx context.Context, username string) (string, error) {
	var teamID string
	err := ts.db.QueryRow(ctx, `
		SELECT t.id FROM teams t
		JOIN team_members tm ON t.id = tm.team_id
		WHERE tm.username = $1 AND t.is_personal = true
	`, username).Scan(&teamID)
	if err != nil {
		return "", fmt.Errorf("personal team not found for user %s: %w", username, err)
	}
	return teamID, nil
}

// CreatePersonalTeam creates a personal team for a user. Used during user creation.
func (ts *TeamStore) CreatePersonalTeam(ctx context.Context, username string) (string, error) {
	id := uuid.New().String()
	now := time.Now().UTC()
	name := username + "'s workspace"

	tx, err := ts.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO teams (id, name, is_personal, created_at) VALUES ($1, $2, true, $3)`,
		id, name, now)
	if err != nil {
		return "", fmt.Errorf("inserting personal team: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO team_members (team_id, username) VALUES ($1, $2)`, id, username)
	if err != nil {
		return "", fmt.Errorf("adding user to personal team: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("committing transaction: %w", err)
	}

	return id, nil
}

// --- Team Settings ---

// GetTeamAgentRepos returns a team's agent repos.
func (ts *TeamStore) GetTeamAgentRepos(ctx context.Context, teamID string) ([]SkillRepo, error) {
	var value json.RawMessage
	err := ts.db.QueryRow(ctx,
		`SELECT value FROM team_settings WHERE team_id = $1 AND key = 'agent_repos'`, teamID).Scan(&value)
	if err != nil {
		return nil, fmt.Errorf("team agent repos not found: %w", err)
	}
	var repos []SkillRepo
	if err := json.Unmarshal(value, &repos); err != nil {
		return nil, fmt.Errorf("unmarshaling team agent repos: %w", err)
	}
	return repos, nil
}

// SetTeamAgentRepos saves a team's agent repos.
func (ts *TeamStore) SetTeamAgentRepos(ctx context.Context, teamID string, repos []SkillRepo) error {
	value, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("marshaling team agent repos: %w", err)
	}
	_, err = ts.db.Exec(ctx, `
		INSERT INTO team_settings (team_id, key, value, updated_at) VALUES ($1, 'agent_repos', $2, $3)
		ON CONFLICT (team_id, key) DO UPDATE SET value = $2, updated_at = $3
	`, teamID, value, time.Now().UTC())
	return err
}

func (ts *TeamStore) GetTeamCatalog(ctx context.Context, teamID string) (map[string]bool, error) {
	var value json.RawMessage
	err := ts.db.QueryRow(ctx, `SELECT value FROM team_settings WHERE team_id = $1 AND key = 'catalog'`, teamID).Scan(&value)
	if err != nil {
		return nil, err
	}
	var catalog map[string]bool
	if err := json.Unmarshal(value, &catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (ts *TeamStore) SetTeamCatalog(ctx context.Context, teamID string, catalog map[string]bool) error {
	value, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	_, err = ts.db.Exec(ctx, `
		INSERT INTO team_settings (team_id, key, value, updated_at) VALUES ($1, 'catalog', $2, $3)
		ON CONFLICT (team_id, key) DO UPDATE SET value = $2, updated_at = $3
	`, teamID, value, time.Now().UTC())
	return err
}

func (ts *TeamStore) GetTeamCustomPlugins(ctx context.Context, teamID string) ([]SkillRepo, error) {
	var value json.RawMessage
	err := ts.db.QueryRow(ctx, `SELECT value FROM team_settings WHERE team_id = $1 AND key = 'custom_plugins'`, teamID).Scan(&value)
	if err != nil {
		return nil, err
	}
	var plugins []SkillRepo
	if err := json.Unmarshal(value, &plugins); err != nil {
		return nil, err
	}
	return plugins, nil
}

func (ts *TeamStore) SetTeamCustomPlugins(ctx context.Context, teamID string, plugins []SkillRepo) error {
	value, err := json.Marshal(plugins)
	if err != nil {
		return err
	}
	_, err = ts.db.Exec(ctx, `
		INSERT INTO team_settings (team_id, key, value, updated_at) VALUES ($1, 'custom_plugins', $2, $3)
		ON CONFLICT (team_id, key) DO UPDATE SET value = $2, updated_at = $3
	`, teamID, value, time.Now().UTC())
	return err
}

// --- HTTP Handlers ---

func (a *API) handleTeams(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		teams, err := a.teamStore.ListTeamsForUser(r.Context(), user)
		if err != nil {
			log.Printf("error: listing teams: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list teams")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"teams": teams,
			"count": len(teams),
		})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.Name == "" {
			respondError(w, http.StatusBadRequest, "name is required")
			return
		}
		team, err := a.teamStore.CreateTeam(r.Context(), req.Name, user)
		if err != nil {
			log.Printf("error: creating team: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to create team: "+err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, team)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleTeam(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/teams/")
	parts := strings.SplitN(path, "/", 2)
	teamID := parts[0]

	if teamID == "" {
		respondError(w, http.StatusBadRequest, "team id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	if user == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	isAdmin := r.Header.Get("X-Alcove-Admin") == "true"

	// Check membership or admin.
	isMember, err := a.teamStore.IsMember(r.Context(), teamID, user)
	if err != nil {
		respondError(w, http.StatusNotFound, "team not found")
		return
	}
	if !isMember && !isAdmin {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	// Handle /api/v1/teams/{id}/catalog/...
	if len(parts) == 2 && strings.HasPrefix(parts[1], "catalog") {
		a.handleTeamCatalogRoute(w, r, teamID, parts[1])
		return
	}

	// Handle /api/v1/teams/{id}/members or /api/v1/teams/{id}/members/{username}
	if len(parts) == 2 && strings.HasPrefix(parts[1], "members") {
		a.handleTeamMembersRoute(w, r, teamID, parts[1])
		return
	}

	switch r.Method {
	case http.MethodGet:
		team, err := a.teamStore.GetTeam(r.Context(), teamID)
		if err != nil {
			respondError(w, http.StatusNotFound, "team not found")
			return
		}
		respondJSON(w, http.StatusOK, team)
	case http.MethodPut:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.Name == "" {
			respondError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := a.teamStore.RenameTeam(r.Context(), teamID, req.Name); err != nil {
			log.Printf("error: renaming team %s: %v", teamID, err)
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"updated": true})
	case http.MethodDelete:
		// Cancel running sessions for this team before deleting.
		_, _ = a.db.Exec(r.Context(),
			`UPDATE sessions SET outcome = 'cancelled', finished_at = NOW() WHERE team_id = $1 AND outcome = 'running'`,
			teamID)

		if err := a.teamStore.DeleteTeam(r.Context(), teamID); err != nil {
			log.Printf("error: deleting team %s: %v", teamID, err)
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleTeamMembersRoute(w http.ResponseWriter, r *http.Request, teamID, membersPath string) {
	// membersPath is "members" or "members/{username}"
	membersParts := strings.SplitN(membersPath, "/", 2)

	if len(membersParts) == 1 {
		// POST /api/v1/teams/{id}/members — add member
		if r.Method != http.MethodPost {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.Username == "" {
			respondError(w, http.StatusBadRequest, "username is required")
			return
		}
		if err := a.teamStore.AddMember(r.Context(), teamID, req.Username); err != nil {
			log.Printf("error: adding member to team %s: %v", teamID, err)
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, map[string]bool{"added": true})
	} else {
		// DELETE /api/v1/teams/{id}/members/{username} — remove member
		username := membersParts[1]
		if r.Method != http.MethodDelete {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if err := a.teamStore.RemoveMember(r.Context(), teamID, username); err != nil {
			log.Printf("error: removing member from team %s: %v", teamID, err)
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"removed": true})
	}
}

func (a *API) handleTeamCatalogRoute(w http.ResponseWriter, r *http.Request, teamID, catalogPath string) {
	subParts := strings.SplitN(catalogPath, "/", 3)
	// subParts[0] = "catalog"
	// subParts[1] = entryId or "custom" (optional)
	// subParts[2] = index for custom delete (optional)

	if len(subParts) == 1 {
		// /teams/{id}/catalog
		switch r.Method {
		case http.MethodGet:
			catalog := LoadCatalog()
			enabledMap, _ := a.teamStore.GetTeamCatalog(r.Context(), teamID)
			if enabledMap == nil {
				enabledMap = make(map[string]bool)
			}
			customPlugins, _ := a.teamStore.GetTeamCustomPlugins(r.Context(), teamID)

			type entryWithStatus struct {
				CatalogEntry
				Enabled bool `json:"enabled"`
			}
			entries := make([]entryWithStatus, len(catalog))
			for i, e := range catalog {
				entries[i] = entryWithStatus{CatalogEntry: e, Enabled: enabledMap[e.ID]}
			}
			respondJSON(w, http.StatusOK, map[string]any{
				"entries":        entries,
				"custom_plugins": customPlugins,
			})
		default:
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	action := subParts[1]

	if action == "custom" {
		switch r.Method {
		case http.MethodPost:
			var repo SkillRepo
			if err := json.NewDecoder(r.Body).Decode(&repo); err != nil {
				respondError(w, http.StatusBadRequest, "invalid request: "+err.Error())
				return
			}
			if repo.URL == "" {
				respondError(w, http.StatusBadRequest, "url is required")
				return
			}
			existing, _ := a.teamStore.GetTeamCustomPlugins(r.Context(), teamID)
			existing = append(existing, repo)
			if err := a.teamStore.SetTeamCustomPlugins(r.Context(), teamID, existing); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to save")
				return
			}
			respondJSON(w, http.StatusCreated, map[string]any{"added": true, "custom_plugins": existing})
		case http.MethodDelete:
			if len(subParts) < 3 {
				respondError(w, http.StatusBadRequest, "index required")
				return
			}
			idx := 0
			fmt.Sscanf(subParts[2], "%d", &idx)
			existing, _ := a.teamStore.GetTeamCustomPlugins(r.Context(), teamID)
			if idx < 0 || idx >= len(existing) {
				respondError(w, http.StatusBadRequest, "invalid index")
				return
			}
			existing = append(existing[:idx], existing[idx+1:]...)
			if err := a.teamStore.SetTeamCustomPlugins(r.Context(), teamID, existing); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to save")
				return
			}
			respondJSON(w, http.StatusOK, map[string]any{"removed": true, "custom_plugins": existing})
		default:
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	// PUT /teams/{id}/catalog/{entryId} — toggle
	entryID := action
	if r.Method != http.MethodPut {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Validate entry exists in catalog
	catalog := LoadCatalog()
	found := false
	for _, e := range catalog {
		if e.ID == entryID {
			found = true
			break
		}
	}
	if !found {
		respondError(w, http.StatusNotFound, "catalog entry not found: "+entryID)
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	enabledMap, _ := a.teamStore.GetTeamCatalog(r.Context(), teamID)
	if enabledMap == nil {
		enabledMap = make(map[string]bool)
	}
	enabledMap[entryID] = req.Enabled
	if err := a.teamStore.SetTeamCatalog(r.Context(), teamID, enabledMap); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true, "entry_id": entryID, "enabled": req.Enabled})
}
