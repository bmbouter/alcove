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
)

// TaskRepoSyncer periodically clones/pulls task repos and syncs YAML task
// definitions and security profiles into the database, reconciling schedules as needed.
type TaskRepoSyncer struct {
	db            *pgxpool.Pool
	settingsStore *SettingsStore
	scheduler     *Scheduler
	defStore      *TaskDefStore
	dispatcher    *Dispatcher
	profileStore  *ProfileStore
	interval      time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
	syncMu        sync.Mutex // prevents concurrent SyncAll calls
}

// NewTaskRepoSyncer creates a TaskRepoSyncer with the given dependencies.
func NewTaskRepoSyncer(db *pgxpool.Pool, settingsStore *SettingsStore, scheduler *Scheduler, defStore *TaskDefStore, dispatcher *Dispatcher, profileStore *ProfileStore) *TaskRepoSyncer {
	interval := 5 * time.Minute
	if v := os.Getenv("TASK_REPO_SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}

	return &TaskRepoSyncer{
		db:            db,
		settingsStore: settingsStore,
		scheduler:     scheduler,
		defStore:      defStore,
		dispatcher:    dispatcher,
		profileStore:  profileStore,
		interval:      interval,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the sync loop in a background goroutine.
func (s *TaskRepoSyncer) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		// Sync immediately on start.
		if err := s.SyncAll(ctx); err != nil {
			log.Printf("task-repo-syncer: initial sync error: %v", err)
		}

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.SyncAll(ctx); err != nil {
					log.Printf("task-repo-syncer: sync error: %v", err)
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
func (s *TaskRepoSyncer) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// SyncAll collects all user task repos and syncs each per-user.
func (s *TaskRepoSyncer) SyncAll(ctx context.Context) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	// Collect per-user task repos.
	type userRepoSet struct {
		username string
		repos    []SkillRepo
	}
	var userRepoSets []userRepoSet
	totalRepos := 0

	// Collect all users who have task_repos configured (even if empty — they may need cleanup).
	allUsersWithRepos := make(map[string][]SkillRepo)
	rows, err := s.db.Query(ctx, `SELECT DISTINCT username FROM user_settings WHERE key = 'task_repos'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var username string
			if err := rows.Scan(&username); err != nil {
				continue
			}
			if userRepos, err := s.settingsStore.GetUserTaskRepos(ctx, username); err == nil {
				allUsersWithRepos[username] = userRepos
				if len(userRepos) > 0 {
					userRepoSets = append(userRepoSets, userRepoSet{username: username, repos: userRepos})
					totalRepos += len(userRepos)
				}
			}
		}
	}

	// Per-user cleanup: remove definitions/profiles for repos the user no longer has configured.
	for username, repos := range allUsersWithRepos {
		configuredURLs := make(map[string]bool)
		for _, repo := range repos {
			configuredURLs[repo.URL] = true
		}
		userDefs, err := s.defStore.ListTaskDefinitions(ctx, username)
		if err == nil {
			removedRepos := make(map[string]bool)
			for _, def := range userDefs {
				if !configuredURLs[def.SourceRepo] && !removedRepos[def.SourceRepo] {
					log.Printf("task-repo-syncer: removing tasks and profiles from %s for user %s (no longer configured)", def.SourceRepo, username)
					_ = s.defStore.DeleteTaskDefinitionsByRepo(ctx, def.SourceRepo, username)
					_ = s.profileStore.DeleteYAMLProfilesByRepo(ctx, def.SourceRepo, username)
					// Also clean up schedules from this repo.
					s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key LIKE $1 AND owner = $2`, username+"::%", username)
					removedRepos[def.SourceRepo] = true
				}
			}
		}
	}

	if totalRepos == 0 {
		return nil
	}

	log.Printf("task-repo-syncer: syncing %d task repo(s) across %d user(s)", totalRepos, len(userRepoSets))

	var errs []string
	for _, urs := range userRepoSets {
		for _, repo := range urs.repos {
			if !repo.IsEnabled() {
				// Repo is disabled — disable all its schedules but don't remove them.
				sourceKeyPrefix := urs.username + "::" + repo.URL + "::"
				_, _ = s.db.Exec(ctx,
					`UPDATE schedules SET enabled = false WHERE source_key LIKE $1 || '%' AND source = 'yaml'`,
					sourceKeyPrefix)
				log.Printf("task-repo-syncer: repo %s disabled for user %s, schedules paused", repo.URL, urs.username)
				continue
			}
			if err := s.syncRepo(ctx, repo, urs.username); err != nil {
				errs = append(errs, fmt.Sprintf("%s (user %s): %v", repo.URL, urs.username, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// syncRepo clones or pulls a single repo and syncs its task definitions for the given user.
func (s *TaskRepoSyncer) syncRepo(ctx context.Context, repo SkillRepo, username string) error {
	// Determine local clone directory.
	name := repo.Name
	if name == "" {
		// Derive name from URL.
		name = filepath.Base(strings.TrimSuffix(repo.URL, ".git"))
	}
	cloneDir := filepath.Join(os.TempDir(), "alcove-task-repos", name)

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
	} else {
		// Remove stale lock files that can be left behind by interrupted git operations.
		for _, lockFile := range []string{"shallow.lock", "index.lock"} {
			os.Remove(filepath.Join(cloneDir, ".git", lockFile))
		}
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
	}

	// Sync security profiles from .alcove/security-profiles/*.yml.
	s.syncSecurityProfiles(ctx, cloneDir, repo, username)

	// Read all .alcove/tasks/*.yml files.
	tasksDir := filepath.Join(cloneDir, ".alcove", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("task-repo-syncer: no .alcove/tasks/ directory in %s", repo.URL)
			// Remove any previously synced definitions from this repo.
			return s.defStore.DeleteTaskDefinitionsByRepo(ctx, repo.URL, username)
		}
		return fmt.Errorf("reading tasks dir: %w", err)
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
			log.Printf("task-repo-syncer: error reading %s: %v", filePath, err)
			continue
		}

		sourceKey := fmt.Sprintf("%s::%s::%s", username, repo.URL, entry.Name())
		seenKeys[sourceKey] = true

		td, err := ParseTaskDefinition(data)
		if err != nil {
			log.Printf("task-repo-syncer: parse error in %s/%s: %v", repo.URL, entry.Name(), err)
			// Store the definition with sync error.
			errDef := &TaskDefinition{
				ID:         uuid.New().String(),
				Name:       entry.Name(),
				SourceRepo: repo.URL,
				SourceFile: entry.Name(),
				SourceKey:  sourceKey,
				RawYAML:    string(data),
				SyncError:  err.Error(),
				Owner:      username,
			}
			_ = s.defStore.UpsertTaskDefinition(ctx, errDef)
			continue
		}

		td.SourceRepo = repo.URL
		td.SourceFile = entry.Name()
		td.SourceKey = sourceKey
		td.RawYAML = string(data)
		td.Owner = username

		if err := s.defStore.UpsertTaskDefinition(ctx, td); err != nil {
			log.Printf("task-repo-syncer: upsert error for %s: %v", sourceKey, err)
			continue
		}

		// Reconcile schedule.
		if err := s.reconcileSchedule(ctx, td, repo.URL, username); err != nil {
			log.Printf("task-repo-syncer: schedule reconcile error for %s: %v", sourceKey, err)
		}
	}

	// Delete definitions that no longer exist in the repo.
	existing, err := s.defStore.ListTaskDefinitionsByRepo(ctx, repo.URL, username)
	if err != nil {
		return fmt.Errorf("listing existing definitions: %w", err)
	}
	for _, def := range existing {
		if !seenKeys[def.SourceKey] {
			// Remove the definition and its schedule.
			if _, err := s.db.Exec(ctx, `DELETE FROM task_definitions WHERE source_key = $1`, def.SourceKey); err != nil {
				log.Printf("task-repo-syncer: error deleting stale definition %s: %v", def.SourceKey, err)
			}
			if _, err := s.db.Exec(ctx, `DELETE FROM schedules WHERE source_key = $1`, def.SourceKey); err != nil {
				log.Printf("task-repo-syncer: error deleting stale schedule for %s: %v", def.SourceKey, err)
			}
		}
	}

	log.Printf("task-repo-syncer: synced %d task(s) from %s", len(seenKeys), repo.URL)

	// Validate profile references in task definitions.
	s.validateProfileReferences(ctx, repo.URL, username)

	return nil
}

// syncSecurityProfiles syncs .alcove/security-profiles/*.yml from a cloned repo for the given user.
func (s *TaskRepoSyncer) syncSecurityProfiles(ctx context.Context, cloneDir string, repo SkillRepo, username string) {
	profilesDir := filepath.Join(cloneDir, ".alcove", "security-profiles")
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("task-repo-syncer: error reading security-profiles dir: %v", err)
		}
		// No profiles dir — clean up any previously synced profiles from this repo.
		_ = s.profileStore.DeleteYAMLProfilesByRepo(ctx, repo.URL, username)
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
			log.Printf("task-repo-syncer: error reading %s: %v", filePath, err)
			continue
		}

		sourceKey := fmt.Sprintf("%s::%s::security-profiles/%s", username, repo.URL, entry.Name())
		seenKeys[sourceKey] = true

		profile, err := ParseSecurityProfile(data)
		if err != nil {
			log.Printf("task-repo-syncer: profile parse error in %s/%s: %v", repo.URL, entry.Name(), err)
			continue
		}

		profile.Source = "yaml"
		profile.SourceRepo = repo.URL
		profile.SourceKey = sourceKey
		profile.Owner = username

		if err := s.profileStore.UpsertYAMLProfile(ctx, profile); err != nil {
			log.Printf("task-repo-syncer: profile upsert error for %s: %v", sourceKey, err)
		}
	}

	// Delete stale YAML profiles from this repo.
	existingKeys, err := s.profileStore.ListYAMLProfileKeysByRepo(ctx, repo.URL, username)
	if err != nil {
		log.Printf("task-repo-syncer: error listing existing profile keys: %v", err)
		return
	}
	for _, key := range existingKeys {
		if !seenKeys[key] {
			if _, err := s.db.Exec(ctx, `DELETE FROM security_profiles WHERE source_key = $1`, key); err != nil {
				log.Printf("task-repo-syncer: error deleting stale profile %s: %v", key, err)
			}
		}
	}

	log.Printf("task-repo-syncer: synced %d security profile(s) from %s", len(seenKeys), repo.URL)
}

// validateProfileReferences checks that all profile references in task definitions
// from the given repo resolve to known profiles. Sets sync_error on definitions
// with unknown profile references.
func (s *TaskRepoSyncer) validateProfileReferences(ctx context.Context, repoURL string, username string) {
	defs, err := s.defStore.ListTaskDefinitionsByRepo(ctx, repoURL, username)
	if err != nil {
		log.Printf("task-repo-syncer: error listing definitions for validation: %v", err)
		return
	}

	for _, def := range defs {
		def.Owner = username // Ensure owner is set for any upserts below.
		if def.SyncError != "" && !strings.HasPrefix(def.SyncError, "unknown security profile:") {
			// Definition has a parse error (not a profile error) — skip validation.
			continue
		}
		if len(def.Profiles) == 0 {
			// No profiles referenced — clear any previous profile error.
			if strings.HasPrefix(def.SyncError, "unknown security profile:") {
				def.SyncError = ""
				_ = s.defStore.UpsertTaskDefinition(ctx, &def)
			}
			continue
		}

		// Check each referenced profile.
		var missing []string
		for _, profileName := range def.Profiles {
			if _, err := s.profileStore.GetProfile(ctx, profileName, username); err != nil {
				missing = append(missing, profileName)
			}
		}

		if len(missing) > 0 {
			syncErr := fmt.Sprintf("unknown security profile: %s", strings.Join(missing, ", "))
			if def.SyncError != syncErr {
				def.SyncError = syncErr
				_ = s.defStore.UpsertTaskDefinition(ctx, &def)
				log.Printf("task-repo-syncer: %s in %s/%s", syncErr, repoURL, def.SourceFile)
			}
		} else if strings.HasPrefix(def.SyncError, "unknown security profile:") {
			// All profiles now exist — clear the error.
			def.SyncError = ""
			_ = s.defStore.UpsertTaskDefinition(ctx, &def)
		}
	}
}

// ValidateRepo clones a repo to a temp directory, checks for .alcove/tasks/*.yml,
// parses each task definition, and returns the task names or an error.
func (s *TaskRepoSyncer) ValidateRepo(ctx context.Context, repo SkillRepo) ([]string, error) {
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
			return nil, fmt.Errorf("invalid task %s: %w", e.Name(), err)
		}
		names = append(names, def.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no valid task definitions found in .alcove/tasks/")
	}
	return names, nil
}

// reconcileSchedule creates, updates, or removes a schedule for a task definition.
func (s *TaskRepoSyncer) reconcileSchedule(ctx context.Context, td *TaskDefinition, repoURL string, username string) error {
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
			INSERT INTO schedules (id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, next_run, created_at, owner, debug, source, source_key, trigger_type, event_config)
			VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8, $9, $10, $15, $11, 'yaml', $12, $13, $14)
		`, uuid.New().String(), td.Name, cronExpr, td.Prompt, td.Repo, td.Provider,
			td.Timeout, enabled, nextRun, now, td.Debug, td.SourceKey, triggerType, eventConfigJSON, username)
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
