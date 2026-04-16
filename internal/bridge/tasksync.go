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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/semaphore"
)

// AgentRepoSyncer periodically clones/pulls agent repos and syncs YAML agent
// definitions, workflow definitions, and security profiles into the database, reconciling schedules as needed.
type AgentRepoSyncer struct {
	db               *pgxpool.Pool
	settingsStore    *SettingsStore
	scheduler        *Scheduler
	defStore         *AgentDefStore
	dispatcher       *Dispatcher
	profileStore     *ProfileStore
	workflowStore    *WorkflowStore
	catalogItemStore *CatalogItemStore
	interval         time.Duration
	stopCh           chan struct{}
	wg               sync.WaitGroup
	syncMu           sync.Mutex // prevents concurrent SyncAll calls
	gitSem           *semaphore.Weighted // limits concurrent git operations
	lastSyncedHash   map[string]string   // repo URL -> last synced remote HEAD hash
}

// NewAgentRepoSyncer creates a AgentRepoSyncer with the given dependencies.
func NewAgentRepoSyncer(db *pgxpool.Pool, settingsStore *SettingsStore, scheduler *Scheduler, defStore *AgentDefStore, dispatcher *Dispatcher, profileStore *ProfileStore, workflowStore *WorkflowStore) *AgentRepoSyncer {
	interval := 15 * time.Minute
	if v := os.Getenv("AGENT_REPO_SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}

	return &AgentRepoSyncer{
		db:               db,
		settingsStore:    settingsStore,
		scheduler:        scheduler,
		defStore:         defStore,
		dispatcher:       dispatcher,
		profileStore:     profileStore,
		workflowStore:    workflowStore,
		catalogItemStore: NewCatalogItemStore(db),
		interval:         interval,
		stopCh:           make(chan struct{}),
		gitSem:           semaphore.NewWeighted(3),
		lastSyncedHash:   make(map[string]string),
	}
}

// Start begins the sync loop in a background goroutine.
func (s *AgentRepoSyncer) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		// Sync immediately on start.
		if err := s.SyncAll(ctx); err != nil {
			log.Printf("agent-repo-syncer: initial sync error: %v", err)
		}

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.SyncAll(ctx); err != nil {
					log.Printf("agent-repo-syncer: sync error: %v", err)
				}
			case <-s.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop signals the syncer goroutine to stop and waits for it to finish.
func (s *AgentRepoSyncer) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// resolveUsernameToTeamID looks up the personal team ID for a username.
func (s *AgentRepoSyncer) resolveUsernameToTeamID(ctx context.Context, username string) string {
	var teamID string
	err := s.db.QueryRow(ctx, `
		SELECT t.id FROM teams t
		JOIN team_members tm ON t.id = tm.team_id
		WHERE tm.username = $1 AND t.is_personal = true
	`, username).Scan(&teamID)
	if err != nil {
		return ""
	}
	return teamID
}

// SyncAll collects all team agent repos and syncs each per-team.
func (s *AgentRepoSyncer) SyncAll(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	type userRepoSet struct {
		username string
		teamID   string
		repos    []SkillRepo
	}
	var userRepoSets []userRepoSet
	totalRepos := 0

	// Track all teams with repos configured (even if empty — they may need cleanup).
	allTeamsWithRepos := make(map[string][]SkillRepo) // teamID -> repos
	// Track which username to use for each team (for credential lookup).
	teamToUsername := make(map[string]string)
	handledTeams := make(map[string]bool)

	// Check team_settings for agent_repos (all teams, not just personal).
	// Pick one member per team to avoid duplicate syncs.
	teamRows, teamErr := s.db.Query(ctx, `
		SELECT DISTINCT ON (ts.team_id) tm.username, ts.team_id, ts.value
		FROM team_settings ts
		JOIN team_members tm ON ts.team_id = tm.team_id
		WHERE ts.key = 'agent_repos'
		ORDER BY ts.team_id, tm.username
	`)
	if teamErr == nil {
		defer teamRows.Close()
		for teamRows.Next() {
			var username, teamID string
			var value json.RawMessage
			if err := teamRows.Scan(&username, &teamID, &value); err != nil {
				continue
			}
			var repos []SkillRepo
			if json.Unmarshal(value, &repos) != nil {
				continue
			}
			allTeamsWithRepos[teamID] = repos
			teamToUsername[teamID] = username
			handledTeams[teamID] = true
			if len(repos) > 0 {
				userRepoSets = append(userRepoSets, userRepoSet{username: username, teamID: teamID, repos: repos})
				totalRepos += len(repos)
			}
		}
	}

	// Fallback: also check user_settings for users not yet migrated.
	rows, err := s.db.Query(ctx, `SELECT DISTINCT username FROM user_settings WHERE key = 'agent_repos'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var username string
			if err := rows.Scan(&username); err != nil {
				continue
			}
			teamID := s.resolveUsernameToTeamID(ctx, username)
			// Skip if this team was already handled via team_settings.
			if handledTeams[teamID] {
				continue
			}
			if userRepos, err := s.settingsStore.GetUserAgentRepos(ctx, username); err == nil {
				allTeamsWithRepos[teamID] = userRepos
				teamToUsername[teamID] = username
				handledTeams[teamID] = true
				if len(userRepos) > 0 {
					userRepoSets = append(userRepoSets, userRepoSet{username: username, teamID: teamID, repos: userRepos})
					totalRepos += len(userRepos)
				}
			}
		}
	}

	// Per-team cleanup: remove definitions/profiles for repos the team no longer has configured.
	for teamID, repos := range allTeamsWithRepos {
		username := teamToUsername[teamID]
		configuredURLs := make(map[string]bool)
		for _, repo := range repos {
			configuredURLs[repo.URL] = true
		}
		teamDefs, err := s.defStore.ListAgentDefinitions(ctx, teamID)
		if err == nil {
			removedRepos := make(map[string]bool)
			for _, def := range teamDefs {
				if !configuredURLs[def.SourceRepo] && !removedRepos[def.SourceRepo] {
					log.Printf("agent-repo-syncer: removing agent definitions, workflows, and profiles from %s for team %s (no longer configured)", def.SourceRepo, teamID)
					_ = s.defStore.DeleteAgentDefinitionsByRepo(ctx, def.SourceRepo, teamID)
					_ = s.profileStore.DeleteYAMLProfilesByRepo(ctx, def.SourceRepo, teamID)
					_ = s.workflowStore.DeleteWorkflowsByRepo(ctx, def.SourceRepo, teamID)
					// Also clean up schedules from this repo.
					s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key LIKE $1 AND team_id = $2`, username+"::%", teamID)
					removedRepos[def.SourceRepo] = true
				}
			}
		}
	}

	// Seed catalog items from embedded catalog data (no git cloning needed).
	s.seedCatalogItemsFromEmbedded(ctx)

	// Migrate source-level enablement to item-level for all teams.
	s.migrateTeamCatalogEnablement(ctx)

	if totalRepos == 0 {
		return nil
	}

	log.Printf("agent-repo-syncer: syncing %d agent repo(s) across %d user(s)", totalRepos, len(userRepoSets))

	var errs []string
	for _, urs := range userRepoSets {
		if urs.teamID == "" {
			continue // Skip users without a team
		}
		for _, repo := range urs.repos {
			if !repo.IsEnabled() {
				// Repo is disabled — disable all its schedules but don't remove them.
				sourceKeyPrefix := urs.username + "::" + repo.URL + "::"
				_, _ = s.db.Exec(ctx,
					`UPDATE schedules SET enabled = false WHERE source_key LIKE $1 || '%' AND source = 'yaml'`,
					sourceKeyPrefix)
				log.Printf("agent-repo-syncer: repo %s disabled for user %s, schedules paused", repo.URL, urs.username)
				continue
			}
			// Throttle concurrent git operations.
			if err := s.gitSem.Acquire(ctx, 1); err != nil {
				errs = append(errs, fmt.Sprintf("%s (user %s): semaphore acquire: %v", repo.URL, urs.username, err))
				continue
			}
			if err := s.syncRepo(ctx, repo, urs.username, urs.teamID); err != nil {
				errs = append(errs, fmt.Sprintf("%s (user %s): %v", repo.URL, urs.username, err))
			}
			s.gitSem.Release(1)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// seedCatalogItemsFromEmbedded builds catalog items directly from the embedded
// catalog.json data, avoiding any git clone/fetch operations. Each catalog entry
// becomes a single CatalogItem keyed by its ID.
func (s *AgentRepoSyncer) seedCatalogItemsFromEmbedded(ctx context.Context) {
	catalog := LoadCatalog()

	// Group items by source ID (each catalog entry is its own source).
	for _, entry := range catalog {
		item := CatalogItem{
			SourceID:    entry.ID,
			Slug:        entry.ID,
			Name:        entry.Name,
			Description: entry.Description,
			ItemType:    categoryToItemType(entry.Category),
			SourceFile:  entry.SourcePath,
		}

		if err := s.catalogItemStore.UpsertCatalogItems(ctx, entry.ID, []CatalogItem{item}); err != nil {
			log.Printf("agent-repo-syncer: error seeding catalog item for %s: %v", entry.ID, err)
		}
	}
}

// categoryToItemType maps a catalog category to an item type.
func categoryToItemType(category string) string {
	switch category {
	case "agent-templates":
		return "agent"
	case "plugins", "language-servers", "integrations", "security":
		return "plugin"
	default:
		return "plugin"
	}
}

// migrateTeamCatalogEnablement migrates source-level catalog enablement to item-level
// for all teams that have the old team_settings catalog key.
func (s *AgentRepoSyncer) migrateTeamCatalogEnablement(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
		SELECT team_id, value FROM team_settings WHERE key = 'catalog'
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var teamID string
		var value json.RawMessage
		if err := rows.Scan(&teamID, &value); err != nil {
			continue
		}
		var enabledMap map[string]bool
		if json.Unmarshal(value, &enabledMap) != nil {
			continue
		}
		if err := s.catalogItemStore.MigrateSourceEnablementToItems(ctx, teamID, enabledMap); err != nil {
			log.Printf("agent-repo-syncer: error migrating catalog enablement for team %s: %v", teamID, err)
		}
	}
}

// syncRepo clones or pulls a single repo and syncs its agent definitions for the given user and team.
func (s *AgentRepoSyncer) syncRepo(ctx context.Context, repo SkillRepo, username, teamID string) error {
	// Determine local clone directory.
	name := repo.Name
	if name == "" {
		// Derive name from URL.
		name = filepath.Base(strings.TrimSuffix(repo.URL, ".git"))
	}
	cloneDir := filepath.Join(os.TempDir(), "alcove-agent-repos", name)

	ref := repo.Ref
	if ref == "" {
		ref = "main"
	}

	// Clone or pull.
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); os.IsNotExist(err) {
		// Fresh clone.
		if err := os.MkdirAll(filepath.Dir(cloneDir), 0755); err != nil {
			return fmt.Errorf("creating clone parent dir: %w", err)
		}
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--branch", ref, repo.URL, cloneDir)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("cloning %s: %s: %w", repo.URL, string(out), err)
		}
		// Record the HEAD hash after clone.
		if headOut, err := exec.CommandContext(ctx, "git", "-C", cloneDir, "rev-parse", "HEAD").Output(); err == nil {
			s.lastSyncedHash[repo.URL] = strings.TrimSpace(string(headOut))
		}
	} else {
		// Remove stale lock files that can be left behind by interrupted git operations.
		for _, lockFile := range []string{"shallow.lock", "index.lock"} {
			os.Remove(filepath.Join(cloneDir, ".git", lockFile))
		}

		// Skip fetch if the remote HEAD hasn't changed since last sync.
		skipFetch := false
		if lastHash, ok := s.lastSyncedHash[repo.URL]; ok && lastHash != "" {
			cmd := exec.CommandContext(ctx, "git", "ls-remote", "--heads", repo.URL, ref)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if out, err := cmd.Output(); err == nil {
				fields := strings.Fields(strings.TrimSpace(string(out)))
				if len(fields) >= 1 && fields[0] == lastHash {
					log.Printf("agent-repo-syncer: repo %s unchanged (HEAD %s), skipping fetch", repo.URL, lastHash[:12])
					skipFetch = true
				}
			}
		}

		if !skipFetch {
			// Pull latest.
			cmd := exec.CommandContext(ctx, "git", "-C", cloneDir, "fetch", "--depth=1", "origin", ref)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("fetching %s: %s: %w", repo.URL, string(out), err)
			}
			cmd = exec.CommandContext(ctx, "git", "-C", cloneDir, "reset", "--hard", "FETCH_HEAD")
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("resetting %s: %s: %w", repo.URL, string(out), err)
			}
			// Record the new HEAD hash after fetch.
			if headOut, err := exec.CommandContext(ctx, "git", "-C", cloneDir, "rev-parse", "HEAD").Output(); err == nil {
				s.lastSyncedHash[repo.URL] = strings.TrimSpace(string(headOut))
			}
		}
	}

	// Sync security profiles from .alcove/security-profiles/*.yml.
	s.syncSecurityProfiles(ctx, cloneDir, repo, username, teamID)

	// Read all .alcove/tasks/*.yml files.
	tasksDir := filepath.Join(cloneDir, ".alcove", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("agent-repo-syncer: no .alcove/tasks/ directory in %s", repo.URL)
			// Remove any previously synced definitions from this repo.
			return s.defStore.DeleteAgentDefinitionsByRepo(ctx, repo.URL, teamID)
		}
		return fmt.Errorf("reading agent definitions dir: %w", err)
	}

	// Track which source_keys we see in this sync.
	seenKeys := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}

		filePath := filepath.Join(tasksDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("agent-repo-syncer: error reading %s: %v", filePath, err)
			continue
		}

		sourceKey := fmt.Sprintf("%s::%s::%s", username, repo.URL, entry.Name())
		seenKeys[sourceKey] = true

		td, err := ParseTaskDefinition(data)
		if err != nil {
			log.Printf("agent-repo-syncer: parse error in %s/%s: %v", repo.URL, entry.Name(), err)
			// Store the definition with sync error.
			errDef := &TaskDefinition{
				ID:         uuid.New().String(),
				Name:       entry.Name(),
				SourceRepo: repo.URL,
				SourceFile: entry.Name(),
				SourceKey:  sourceKey,
				RawYAML:    string(data),
				SyncError:  err.Error(),
				TeamID:      teamID,
			}
			_ = s.defStore.UpsertAgentDefinition(ctx, errDef)
			continue
		}

		td.SourceRepo = repo.URL
		td.SourceFile = entry.Name()
		td.SourceKey = sourceKey
		td.RawYAML = string(data)
		td.TeamID = teamID

		if err := s.defStore.UpsertAgentDefinition(ctx, td); err != nil {
			log.Printf("agent-repo-syncer: upsert error for %s: %v", sourceKey, err)
			continue
		}

		// Reconcile schedule.
		if err := s.reconcileSchedule(ctx, td, repo.URL, username, teamID); err != nil {
			log.Printf("agent-repo-syncer: schedule reconcile error for %s: %v", sourceKey, err)
		}
	}

	// Delete definitions that no longer exist in the repo.
	existing, err := s.defStore.ListAgentDefinitionsByRepo(ctx, repo.URL, teamID)
	if err != nil {
		return fmt.Errorf("listing existing definitions: %w", err)
	}
	for _, def := range existing {
		if !seenKeys[def.SourceKey] {
			// Remove the definition and its schedule.
			if _, err := s.db.Exec(ctx, `DELETE FROM agent_definitions WHERE source_key = $1`, def.SourceKey); err != nil {
				log.Printf("agent-repo-syncer: error deleting stale definition %s: %v", def.SourceKey, err)
			}
			if _, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key = $1`, def.SourceKey); err != nil {
				log.Printf("agent-repo-syncer: error deleting stale schedule for %s: %v", def.SourceKey, err)
			}
		}
	}

	log.Printf("agent-repo-syncer: synced %d agent definition(s) from %s", len(seenKeys), repo.URL)

	// Sync workflow definitions from .alcove/workflows/*.yml.
	if err := s.syncWorkflowDefinitions(ctx, cloneDir, repo, username, teamID); err != nil {
		log.Printf("agent-repo-syncer: workflow sync error for %s: %v", repo.URL, err)
	}

	// Validate profile references in agent definitions.
	s.validateProfileReferences(ctx, repo.URL, teamID)

	return nil
}

// syncSecurityProfiles syncs .alcove/security-profiles/*.yml from a cloned repo for the given user.
func (s *AgentRepoSyncer) syncSecurityProfiles(ctx context.Context, cloneDir string, repo SkillRepo, username, teamID string) {
	profilesDir := filepath.Join(cloneDir, ".alcove", "security-profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("agent-repo-syncer: error reading security-profiles dir: %v", err)
		}
		// No profiles dir — clean up any previously synced profiles from this repo.
		_ = s.profileStore.DeleteYAMLProfilesByRepo(ctx, repo.URL, teamID)
		return
	}

	seenKeys := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}

		filePath := filepath.Join(profilesDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("agent-repo-syncer: error reading %s: %v", filePath, err)
			continue
		}

		sourceKey := fmt.Sprintf("%s::%s::security-profiles/%s", username, repo.URL, entry.Name())
		seenKeys[sourceKey] = true

		profile, err := ParseSecurityProfile(data)
		if err != nil {
			log.Printf("agent-repo-syncer: profile parse error in %s/%s: %v", repo.URL, entry.Name(), err)
			continue
		}

		profile.Source = "yaml"
		profile.SourceRepo = repo.URL
		profile.SourceKey = sourceKey
		profile.TeamID = teamID

		if err := s.profileStore.UpsertYAMLProfile(ctx, profile); err != nil {
			log.Printf("agent-repo-syncer: profile upsert error for %s: %v", sourceKey, err)
		}
	}

	// Delete stale YAML profiles from this repo.
	existingKeys, err := s.profileStore.ListYAMLProfileKeysByRepo(ctx, repo.URL, teamID)
	if err != nil {
		log.Printf("agent-repo-syncer: error listing existing profile keys: %v", err)
		return
	}
	for _, key := range existingKeys {
		if !seenKeys[key] {
			if _, err := s.db.Exec(ctx, `DELETE FROM security_profiles WHERE source_key = $1`, key); err != nil {
				log.Printf("agent-repo-syncer: error deleting stale profile %s: %v", key, err)
			}
		}
	}

	log.Printf("agent-repo-syncer: synced %d security profile(s) from %s", len(seenKeys), repo.URL)
}

// validateProfileReferences checks that all profile references in agent definitions
// from the given repo resolve to known profiles. Sets sync_error on definitions
// with unknown profile references.
func (s *AgentRepoSyncer) validateProfileReferences(ctx context.Context, repoURL string, teamID string) {
	defs, err := s.defStore.ListAgentDefinitionsByRepo(ctx, repoURL, teamID)
	if err != nil {
		log.Printf("agent-repo-syncer: error listing definitions for validation: %v", err)
		return
	}

	for _, def := range defs {
		def.TeamID = teamID // Ensure team_id is set for any upserts below.
		if def.SyncError != "" && !strings.HasPrefix(def.SyncError, "unknown security profile:") {
			// Definition has a parse error (not a profile error) — skip validation.
			continue
		}
		if len(def.Profiles) == 0 {
			// No profiles referenced — clear any previous profile error.
			if strings.HasPrefix(def.SyncError, "unknown security profile:") {
				def.SyncError = ""
				_ = s.defStore.UpsertAgentDefinition(ctx, &def)
			}
			continue
		}

		// Check each referenced profile.
		var missing []string
		for _, profileName := range def.Profiles {
			if _, err := s.profileStore.GetProfile(ctx, profileName, teamID); err != nil {
				missing = append(missing, profileName)
			}
		}

		if len(missing) > 0 {
			syncErr := fmt.Sprintf("unknown security profile: %s", strings.Join(missing, ", "))
			if def.SyncError != syncErr {
				def.SyncError = syncErr
				_ = s.defStore.UpsertAgentDefinition(ctx, &def)
				log.Printf("agent-repo-syncer: %s in %s/%s", syncErr, repoURL, def.SourceFile)
			}
		} else if strings.HasPrefix(def.SyncError, "unknown security profile:") {
			// All profiles now exist — clear the error.
			def.SyncError = ""
			_ = s.defStore.UpsertAgentDefinition(ctx, &def)
		}
	}
}

// ValidateRepo clones a repo to a temp directory, checks for .alcove/tasks/*.yml,
// parses each agent definition, and returns the names or an error.
func (s *AgentRepoSyncer) ValidateRepo(ctx context.Context, repo SkillRepo) ([]string, error) {
	dir, err := os.MkdirTemp("", "alcove-validate-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	args := []string{"clone", "--depth=1"}
	if repo.Ref != "" {
		args = append(args, "--branch", repo.Ref)
	}
	args = append(args, repo.URL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("cloning: %s", string(out))
	}

	tasksDir := filepath.Join(dir, ".alcove", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, fmt.Errorf("no .alcove/tasks/ directory found")
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml")) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tasksDir, e.Name()))
		if err != nil {
			continue
		}
		def, err := ParseTaskDefinition(data)
		if err != nil {
			return nil, fmt.Errorf("invalid agent definition %s: %w", e.Name(), err)
		}
		names = append(names, def.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no valid agent definitions found in .alcove/tasks/")
	}
	return names, nil
}

// reconcileSchedule creates, updates, or removes a schedule for an agent definition.
func (s *AgentRepoSyncer) reconcileSchedule(ctx context.Context, td *TaskDefinition, repoURL string, username, teamID string) error {
	hasCron := td.Schedule != nil
	hasTrigger := td.Trigger != nil

	if !hasCron && !hasTrigger {
		// No schedule or trigger in YAML — remove any existing YAML-sourced schedule.
		_, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key = $1 AND source = 'yaml'`, td.SourceKey)
		return err
	}

	// Determine trigger_type.
	triggerType := "cron"
	if hasCron && hasTrigger {
		triggerType = "cron-and-event"
	} else if hasTrigger && !hasCron {
		triggerType = "event"
	}

	// Marshal event config if present.
	var eventConfigJSON []byte
	if td.Trigger != nil {
		var err error
		eventConfigJSON, err = json.Marshal(td.Trigger)
		if err != nil {
			return fmt.Errorf("marshaling event config: %w", err)
		}
	}

	// Compute cron and next_run if applicable.
	var cronExpr string
	var nextRun *time.Time
	var enabled bool

	if hasCron {
		cronExpr = td.Schedule.Cron
		enabled = td.Schedule.Enabled
		parsed, err := ParseCron(cronExpr)
		if err != nil {
			return fmt.Errorf("invalid cron: %w", err)
		}
		now := time.Now().UTC()
		nr := parsed.Next(now)
		nextRun = &nr
	} else {
		// Event-only: enabled by default, no cron.
		enabled = true
	}

	// Check if a schedule with this source_key already exists.
	var existingID string
	err := s.db.QueryRow(ctx, `SELECT id FROM schedules WHERE source_key = $1 AND source = 'yaml'`, td.SourceKey).Scan(&existingID)

	if err != nil {
		// No existing schedule — create one.
		now := time.Now().UTC()
		_, err = s.db.Exec(ctx, `
			INSERT INTO schedules (id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, next_run, created_at, team_id, debug, source, source_key, trigger_type, event_config)
			VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8, $9, $10, $15, $11, 'yaml', $12, $13, $14)
		`, uuid.New().String(), td.Name, cronExpr, td.Prompt, td.Repo, td.Provider,
			td.Timeout, enabled, nextRun, now, td.Debug, td.SourceKey, triggerType, eventConfigJSON, teamID)
		return err
	}

	// Existing schedule — update it.
	_, err = s.db.Exec(ctx, `
		UPDATE schedules SET name = $1, cron = $2, prompt = $3, repo = $4, provider = $5,
		    timeout = $6, enabled = $7, next_run = $8, debug = $9,
		    trigger_type = $10, event_config = $11
		WHERE id = $12 AND source = 'yaml'
	`, td.Name, cronExpr, td.Prompt, td.Repo, td.Provider,
		td.Timeout, enabled, nextRun, td.Debug, triggerType, eventConfigJSON, existingID)
	return err
}

// syncWorkflowDefinitions syncs .alcove/workflows/*.yml from a cloned repo for the given user.
func (s *AgentRepoSyncer) syncWorkflowDefinitions(ctx context.Context, cloneDir string, repo SkillRepo, username, teamID string) error {
	workflowsDir := filepath.Join(cloneDir, ".alcove", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("agent-repo-syncer: no .alcove/workflows/ directory in %s", repo.URL)
			// Remove any previously synced workflows from this repo.
			return s.workflowStore.DeleteWorkflowsByRepo(ctx, repo.URL, teamID)
		}
		return fmt.Errorf("reading workflows dir: %w", err)
	}

	// Track which source_keys we see in this sync.
	seenKeys := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml")) {
			continue
		}

		filePath := filepath.Join(workflowsDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("agent-repo-syncer: error reading %s: %v", filePath, err)
			continue
		}

		sourceKey := fmt.Sprintf("%s::%s::%s", username, repo.URL, entry.Name())
		seenKeys[sourceKey] = true

		wd, err := ParseWorkflowDefinition(data)
		if err != nil {
			log.Printf("agent-repo-syncer: workflow parse error in %s/%s: %v", repo.URL, entry.Name(), err)
			// Store the workflow with sync error.
			errWorkflow := &WorkflowDefinition{
				Name:       entry.Name(),
				SourceRepo: repo.URL,
				SourceFile: entry.Name(),
				TeamID:      teamID,
			}
			_ = s.workflowStore.UpsertWorkflow(ctx, errWorkflow, sourceKey, string(data), err.Error())
			continue
		}

		wd.SourceRepo = repo.URL
		wd.SourceFile = entry.Name()
		wd.TeamID = teamID

		if err := s.workflowStore.UpsertWorkflow(ctx, wd, sourceKey, string(data), ""); err != nil {
			log.Printf("agent-repo-syncer: workflow upsert error for %s: %v", sourceKey, err)
			continue
		}

		// Register workflow trigger if present.
		if err := s.reconcileWorkflowTrigger(ctx, wd, sourceKey, username, teamID); err != nil {
			log.Printf("agent-repo-syncer: workflow trigger reconcile error for %s: %v", sourceKey, err)
		}
	}

	// Delete workflows that no longer exist in the repo.
	existing, err := s.workflowStore.ListWorkflowsByRepo(ctx, repo.URL, teamID)
	if err != nil {
		return fmt.Errorf("listing existing workflows: %w", err)
	}
	for _, wf := range existing {
		if !seenKeys[wf.SourceKey] {
			// Remove the workflow and its schedule.
			if _, err := s.db.Exec(ctx, `DELETE FROM workflows WHERE source_key = $1`, wf.SourceKey); err != nil {
				log.Printf("agent-repo-syncer: error deleting stale workflow %s: %v", wf.SourceKey, err)
			}
			if _, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key = $1`, wf.SourceKey); err != nil {
				log.Printf("agent-repo-syncer: error deleting stale workflow schedule for %s: %v", wf.SourceKey, err)
			}
		}
	}

	log.Printf("agent-repo-syncer: synced %d workflow(s) from %s", len(seenKeys), repo.URL)

	// Validate agent references in workflows.
	s.validateWorkflowAgentReferences(ctx, repo.URL, teamID)

	return nil
}

// validateWorkflowAgentReferences checks that all agents referenced in workflows
// from the given repo resolve to known agent definitions or catalog items.
// Sets sync_error on workflows with unknown agent references.
func (s *AgentRepoSyncer) validateWorkflowAgentReferences(ctx context.Context, repoURL string, teamID string) {
	workflows, err := s.workflowStore.ListWorkflowsByRepo(ctx, repoURL, teamID)
	if err != nil {
		log.Printf("agent-repo-syncer: error listing workflows for validation: %v", err)
		return
	}

	// Get all agent definitions for this user (across all repos).
	agentDefs, err := s.defStore.ListAgentDefinitions(ctx, teamID)
	if err != nil {
		log.Printf("agent-repo-syncer: error listing agent definitions for validation: %v", err)
		return
	}

	for _, wf := range workflows {
		if wf.SyncError != "" && !strings.HasPrefix(wf.SyncError, "unknown agent:") && !strings.HasPrefix(wf.SyncError, "Step '") {
			// Workflow has a parse error (not an agent error) — skip validation.
			continue
		}

		// Check each referenced agent, including catalog item lookups.
		missing := s.validateWorkflowAgentsWithCatalog(ctx, &wf.WorkflowDefinition, agentDefs, teamID)

		if len(missing) > 0 {
			syncErr := strings.Join(missing, "; ")
			if wf.SyncError != syncErr {
				// Update the workflow with the agent validation error.
				if err := s.workflowStore.UpsertWorkflow(ctx, &wf.WorkflowDefinition, wf.SourceKey, wf.RawYAML, syncErr); err != nil {
					log.Printf("agent-repo-syncer: error updating workflow sync error for %s: %v", wf.SourceKey, err)
				}
				log.Printf("agent-repo-syncer: %s in %s/%s", syncErr, repoURL, wf.SourceFile)
			}
		} else if strings.HasPrefix(wf.SyncError, "unknown agent:") || strings.HasPrefix(wf.SyncError, "Step '") {
			// All agents now exist — clear the error.
			if err := s.workflowStore.UpsertWorkflow(ctx, &wf.WorkflowDefinition, wf.SourceKey, wf.RawYAML, ""); err != nil {
				log.Printf("agent-repo-syncer: error clearing workflow sync error for %s: %v", wf.SourceKey, err)
			}
		}
	}
}

// validateWorkflowAgentsWithCatalog checks all agent references in a workflow,
// looking in both agent_definitions and catalog_items. Returns error messages for
// each unresolved reference.
func (s *AgentRepoSyncer) validateWorkflowAgentsWithCatalog(ctx context.Context, wd *WorkflowDefinition, agentDefs []TaskDefinition, teamID string) []string {
	agentNames := make(map[string]bool)
	for _, def := range agentDefs {
		agentNames[def.Name] = true
	}

	var errors []string
	for _, step := range wd.Workflow {
		if step.Type == "bridge" || step.Agent == "" {
			continue
		}

		// Try existing agent definitions lookup by name.
		if agentNames[step.Agent] {
			continue
		}

		// If agent reference contains "/", try catalog_items lookup.
		if strings.Contains(step.Agent, "/") {
			parts := strings.SplitN(step.Agent, "/", 2)
			sourceID, slug := parts[0], parts[1]
			item, err := s.catalogItemStore.GetCatalogItem(ctx, sourceID, slug)
			if err != nil {
				errors = append(errors, fmt.Sprintf("Step '%s' references unknown agent '%s'", step.ID, step.Agent))
				continue
			}

			// Check if the item is enabled for this team.
			var enabled bool
			err = s.db.QueryRow(ctx, `
				SELECT enabled FROM team_catalog_items
				WHERE team_id = $1 AND source_id = $2 AND item_slug = $3
			`, teamID, sourceID, slug).Scan(&enabled)
			if err != nil || !enabled {
				errors = append(errors, fmt.Sprintf("Step '%s' references disabled agent '%s' -- enable it in the catalog", step.ID, step.Agent))
				continue
			}

			// Item exists and is enabled — verify it's an agent type.
			if item.ItemType != "agent" {
				errors = append(errors, fmt.Sprintf("Step '%s' references '%s' which is a %s, not an agent", step.ID, step.Agent, item.ItemType))
			}
			continue
		}

		errors = append(errors, fmt.Sprintf("Step '%s' references unknown agent '%s'", step.ID, step.Agent))
	}

	return errors
}

// reconcileWorkflowTrigger creates, updates, or removes a schedule for a workflow trigger.
func (s *AgentRepoSyncer) reconcileWorkflowTrigger(ctx context.Context, wd *WorkflowDefinition, sourceKey string, username, teamID string) error {
	if wd.Trigger == nil {
		// No trigger in YAML — remove any existing YAML-sourced schedule for this workflow.
		_, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key = $1 AND source = 'yaml'`, sourceKey)
		return err
	}

	// Marshal event config.
	eventConfigJSON, err := json.Marshal(wd.Trigger)
	if err != nil {
		return fmt.Errorf("marshaling event config: %w", err)
	}

	// Workflow triggers are event-only (no cron scheduling).
	triggerType := "event"
	enabled := true

	// Check if a schedule with this source_key already exists.
	var existingID string
	err = s.db.QueryRow(ctx, `SELECT id FROM schedules WHERE source_key = $1 AND source = 'yaml'`, sourceKey).Scan(&existingID)

	if err != nil {
		// No existing schedule — create one.
		now := time.Now().UTC()
		// For workflow triggers, we create a schedule that points to the workflow (not an agent definition)
		_, err = s.db.Exec(ctx, `
			INSERT INTO schedules (id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, next_run, created_at, team_id, debug, source, source_key, trigger_type, event_config)
			VALUES ($1, $2, '', '', '', 'workflow', '', 0, $3, NULL, $4, $5, false, 'yaml', $6, $7, $8)
		`, uuid.New().String(), wd.Name, enabled, now, teamID, sourceKey, triggerType, eventConfigJSON)
		return err
	}

	// Existing schedule — update it.
	_, err = s.db.Exec(ctx, `
		UPDATE schedules SET name = $1, enabled = $2, trigger_type = $3, event_config = $4
		WHERE id = $5 AND source = 'yaml'
	`, wd.Name, enabled, triggerType, eventConfigJSON, existingID)
	return err
}
