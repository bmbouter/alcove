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
// definitions into the database, reconciling schedules as needed.
type TaskRepoSyncer struct {
	db            *pgxpool.Pool
	settingsStore *SettingsStore
	scheduler     *Scheduler
	defStore      *TaskDefStore
	dispatcher    *Dispatcher
	interval      time.Duration
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewTaskRepoSyncer creates a TaskRepoSyncer with the given dependencies.
func NewTaskRepoSyncer(db *pgxpool.Pool, settingsStore *SettingsStore, scheduler *Scheduler, defStore *TaskDefStore, dispatcher *Dispatcher) *TaskRepoSyncer {
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

// SyncAll collects all task repos (system + all users) and syncs each.
func (s *TaskRepoSyncer) SyncAll(ctx context.Context) error {
	var repos []SkillRepo

	// System task repos.
	if systemRepos, err := s.settingsStore.GetSystemTaskRepos(ctx); err == nil {
		repos = append(repos, systemRepos...)
	}

	// User task repos from all users.
	rows, err := s.db.Query(ctx, `SELECT DISTINCT username FROM user_settings WHERE key = 'task_repos'`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var username string
			if err := rows.Scan(&username); err != nil {
				continue
			}
			if userRepos, err := s.settingsStore.GetUserTaskRepos(ctx, username); err == nil {
				repos = append(repos, userRepos...)
			}
		}
	}

	if len(repos) == 0 {
		return nil
	}

	log.Printf("task-repo-syncer: syncing %d task repo(s)", len(repos))

	var errs []string
	for _, repo := range repos {
		if err := s.syncRepo(ctx, repo); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", repo.URL, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// syncRepo clones or pulls a single repo and syncs its task definitions.
func (s *TaskRepoSyncer) syncRepo(ctx context.Context, repo SkillRepo) error {
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

	// Read all .alcove/tasks/*.yml files.
	tasksDir := filepath.Join(cloneDir, ".alcove", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("task-repo-syncer: no .alcove/tasks/ directory in %s", repo.URL)
			// Remove any previously synced definitions from this repo.
			return s.defStore.DeleteTaskDefinitionsByRepo(ctx, repo.URL)
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

		sourceKey := fmt.Sprintf("%s::%s", repo.URL, entry.Name())
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
			}
			_ = s.defStore.UpsertTaskDefinition(ctx, errDef)
			continue
		}

		td.SourceRepo = repo.URL
		td.SourceFile = entry.Name()
		td.SourceKey = sourceKey
		td.RawYAML = string(data)

		if err := s.defStore.UpsertTaskDefinition(ctx, td); err != nil {
			log.Printf("task-repo-syncer: upsert error for %s: %v", sourceKey, err)
			continue
		}

		// Reconcile schedule.
		if err := s.reconcileSchedule(ctx, td, repo.URL); err != nil {
			log.Printf("task-repo-syncer: schedule reconcile error for %s: %v", sourceKey, err)
		}
	}

	// Delete definitions that no longer exist in the repo.
	existing, err := s.defStore.ListTaskDefinitionsByRepo(ctx, repo.URL)
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
	return nil
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
func (s *TaskRepoSyncer) reconcileSchedule(ctx context.Context, td *TaskDefinition, repoURL string) error {
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
			VALUES ($1, $2, $3, $4, $5, $6, '', $7, $8, $9, $10, '_system', $11, 'yaml', $12, $13, $14)
		`, uuid.New().String(), td.Name, cronExpr, td.Prompt, td.Repo, td.Provider,
			td.Timeout, enabled, nextRun, now, td.Debug, td.SourceKey, triggerType, eventConfigJSON)
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
