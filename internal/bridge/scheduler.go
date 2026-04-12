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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schedule defines a recurring task.
type Schedule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Cron        string     `json:"cron"`          // cron expression (5-field: min hour dom month dow)
	Prompt      string     `json:"prompt"`
	Repo        string     `json:"repo,omitempty"`
	Provider    string     `json:"provider,omitempty"`
	ScopePreset string     `json:"scope_preset,omitempty"`
	Timeout     int        `json:"timeout,omitempty"` // seconds
	Enabled     bool       `json:"enabled"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	NextRun     *time.Time `json:"next_run,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	Owner       string     `json:"owner,omitempty"`
	Debug       bool          `json:"debug,omitempty"`
	Source      string        `json:"source,omitempty"`
	SourceKey   string        `json:"source_key,omitempty"`
	TriggerType  string        `json:"trigger_type,omitempty"`
	EventConfig  *EventTrigger `json:"event_config,omitempty"`
	RepoDisabled bool          `json:"repo_disabled"`
}

// CronExpr represents a parsed 5-field cron expression.
type CronExpr struct {
	minutes    []int // 0-59
	hours      []int // 0-23
	daysOfMonth []int // 1-31
	months     []int // 1-12
	daysOfWeek []int // 0-6 (0=Sunday)
}

// ParseCron parses a 5-field cron expression (minute hour day-of-month month day-of-week).
// Supported syntax: exact values, wildcards (*), step values (*/5), ranges (1-5), lists (1,3,5).
func ParseCron(expr string) (*CronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d", len(fields))
	}

	minutes, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute field: %w", err)
	}
	hours, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour field: %w", err)
	}
	daysOfMonth, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("day-of-month field: %w", err)
	}
	months, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month field: %w", err)
	}
	daysOfWeek, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("day-of-week field: %w", err)
	}

	return &CronExpr{
		minutes:     minutes,
		hours:       hours,
		daysOfMonth: daysOfMonth,
		months:      months,
		daysOfWeek:  daysOfWeek,
	}, nil
}

// parseField parses a single cron field into a sorted slice of valid values.
func parseField(field string, min, max int) ([]int, error) {
	var result []int
	parts := strings.Split(field, ",")
	for _, part := range parts {
		vals, err := parsePart(part, min, max)
		if err != nil {
			return nil, err
		}
		result = append(result, vals...)
	}

	// Deduplicate and sort.
	seen := make(map[int]bool)
	unique := result[:0]
	for _, v := range result {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}

	// Simple insertion sort (fields are small).
	for i := 1; i < len(unique); i++ {
		for j := i; j > 0 && unique[j] < unique[j-1]; j-- {
			unique[j], unique[j-1] = unique[j-1], unique[j]
		}
	}

	if len(unique) == 0 {
		return nil, fmt.Errorf("field %q produced no values", field)
	}

	return unique, nil
}

// parsePart handles a single part of a cron field: *, */N, N-M, N-M/S, or N.
func parsePart(part string, min, max int) ([]int, error) {
	// Handle step: */N or N-M/S
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		s, err := strconv.Atoi(part[idx+1:])
		if err != nil || s <= 0 {
			return nil, fmt.Errorf("invalid step in %q", part)
		}
		step = s
		part = part[:idx]
	}

	var lo, hi int
	if part == "*" {
		lo, hi = min, max
	} else if idx := strings.Index(part, "-"); idx >= 0 {
		var err error
		lo, err = strconv.Atoi(part[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid range start in %q", part)
		}
		hi, err = strconv.Atoi(part[idx+1:])
		if err != nil {
			return nil, fmt.Errorf("invalid range end in %q", part)
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("range %d-%d out of bounds [%d, %d]", lo, hi, min, max)
		}
	} else {
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid value %q", part)
		}
		if v < min || v > max {
			return nil, fmt.Errorf("value %d out of bounds [%d, %d]", v, min, max)
		}
		if step == 1 {
			return []int{v}, nil
		}
		lo, hi = v, max
	}

	var vals []int
	for i := lo; i <= hi; i += step {
		vals = append(vals, i)
	}
	return vals, nil
}

// Next returns the next time after `after` that matches the cron expression.
func (c *CronExpr) Next(after time.Time) time.Time {
	// Start from the next minute.
	t := after.Truncate(time.Minute).Add(time.Minute)

	// Search up to 4 years (covers all cron cycles including leap years).
	limit := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(limit) {
		if !contains(c.months, int(t.Month())) {
			// Advance to the first day of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !contains(c.daysOfMonth, t.Day()) || !contains(c.daysOfWeek, int(t.Weekday())) {
			// Advance to the next day.
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !contains(c.hours, t.Hour()) {
			// Advance to the next hour.
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !contains(c.minutes, t.Minute()) {
			// Advance by one minute.
			t = t.Add(time.Minute)
			continue
		}
		return t
	}

	// Should not happen for valid cron expressions, but return zero time as fallback.
	return time.Time{}
}

func contains(vals []int, v int) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

// Scheduler runs recurring tasks based on cron schedules stored in PostgreSQL
// and polls GitHub Events API for event-triggered schedules.
type Scheduler struct {
	db            *pgxpool.Pool
	dispatcher    *Dispatcher
	cfg           *Config
	settingsStore *SettingsStore
	poller        *GitHubPoller
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewScheduler creates a Scheduler with the given dependencies.
func NewScheduler(db *pgxpool.Pool, dispatcher *Dispatcher, cfg *Config, credStore *CredentialStore, defStore *AgentDefStore, settingsStore *SettingsStore) *Scheduler {
	return &Scheduler{
		db:            db,
		dispatcher:    dispatcher,
		cfg:           cfg,
		settingsStore: settingsStore,
		poller: &GitHubPoller{
			db:         db,
			dispatcher: dispatcher,
			credStore:  credStore,
			defStore:   defStore,
			client:     &http.Client{Timeout: 30 * time.Second},
		},
		stopCh: make(chan struct{}),
	}
}

// Start begins the scheduler loop in a background goroutine. It checks for
// due schedules every 60 seconds and dispatches tasks accordingly.
func (s *Scheduler) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		// Clear stale GitHub poll ETags on startup. After a deployment or
		// restart, the stored ETag may cause permanent 304 responses from
		// GitHub's CDN. Clearing forces a fresh full poll on the first tick.
		// The first-poll skip logic (no lastEventID) prevents duplicate dispatches.
		_, _ = s.db.Exec(ctx, `UPDATE github_poll_state SET etag = ''`)
		log.Printf("scheduler: cleared GitHub poll ETags on startup")

		// Run immediately on start, then every tick.
		s.tick(ctx)

		for {
			select {
			case <-ticker.C:
				s.tick(ctx)
			case <-s.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop signals the scheduler goroutine to stop and waits for it to finish.
func (s *Scheduler) Stop() {
	close(s.stopCh)
	s.wg.Wait()
}

// tick queries enabled schedules and dispatches any that are due.
func (s *Scheduler) tick(ctx context.Context) {
	// Check system mode — skip dispatch when paused.
	if mode, _ := s.settingsStore.GetSystemMode(ctx); mode == "paused" {
		return
	}

	now := time.Now().UTC()

	rows, err := s.db.Query(ctx, `
		SELECT id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, last_run, next_run, created_at, owner, debug, source, source_key, trigger_type, event_config
		FROM schedules
		WHERE enabled = true AND next_run <= $1
		  AND COALESCE(trigger_type, 'cron') IN ('cron', 'cron-and-event')
	`, now)
	if err != nil {
		log.Printf("scheduler: error querying schedules: %v", err)
		return
	}
	defer rows.Close()

	var due []Schedule
	for rows.Next() {
		var sched Schedule
		var source, sourceKey, triggerType *string
		var eventConfigJSON []byte
		if err := rows.Scan(
			&sched.ID, &sched.Name, &sched.Cron, &sched.Prompt,
			&sched.Repo, &sched.Provider, &sched.ScopePreset, &sched.Timeout,
			&sched.Enabled, &sched.LastRun, &sched.NextRun, &sched.CreatedAt, &sched.Owner, &sched.Debug,
			&source, &sourceKey, &triggerType, &eventConfigJSON,
		); err != nil {
			log.Printf("scheduler: error scanning schedule row: %v", err)
			continue
		}
		if source != nil {
			sched.Source = *source
		}
		if sourceKey != nil {
			sched.SourceKey = *sourceKey
		}
		if triggerType != nil {
			sched.TriggerType = *triggerType
		}
		if eventConfigJSON != nil {
			var ec EventTrigger
			if json.Unmarshal(eventConfigJSON, &ec) == nil {
				sched.EventConfig = &ec
			}
		}
		due = append(due, sched)
	}
	if err := rows.Err(); err != nil {
		log.Printf("scheduler: error iterating schedule rows: %v", err)
		return
	}

	for _, sched := range due {
		log.Printf("scheduler: dispatching schedule %s (%s)", sched.Name, sched.ID)

		req := TaskRequest{
			Prompt:      sched.Prompt,
			Repo:        sched.Repo,
			Provider:    sched.Provider,
			Timeout:     sched.Timeout,
			Debug:       sched.Debug,
			TaskName:    sched.Name,
			TriggerType: "cron",
		}

		if _, err := s.dispatcher.DispatchTask(ctx, req, sched.Owner); err != nil {
			log.Printf("scheduler: error dispatching schedule %s: %v", sched.ID, err)
			continue
		}

		// Update last_run and compute next_run.
		cronExpr, err := ParseCron(sched.Cron)
		if err != nil {
			log.Printf("scheduler: error parsing cron for schedule %s: %v", sched.ID, err)
			continue
		}
		nextRun := cronExpr.Next(now)
		_, err = s.db.Exec(ctx, `
			UPDATE schedules SET last_run = $1, next_run = $2 WHERE id = $3
		`, now, nextRun, sched.ID)
		if err != nil {
			log.Printf("scheduler: error updating schedule %s: %v", sched.ID, err)
		}
	}

	// Poll GitHub Events API for polling-mode event schedules.
	s.poller.PollAll(ctx)
}

// CreateSchedule inserts a new schedule into the database. It generates a UUID
// if sched.ID is empty, validates the cron expression, computes the initial
// next_run, and sets created_at.
func (s *Scheduler) CreateSchedule(ctx context.Context, sched *Schedule, owner string) error {
	if sched.ID == "" {
		sched.ID = uuid.New().String()
	}

	// Determine trigger type.
	triggerType := sched.TriggerType
	if triggerType == "" {
		triggerType = "cron"
	}

	// Cron is required for cron and cron-and-event types, optional for event-only.
	if triggerType == "event" {
		// Event-only schedules don't need cron or next_run.
		now := time.Now().UTC()
		sched.CreatedAt = now
		sched.Owner = owner
	} else {
		cronExpr, err := ParseCron(sched.Cron)
		if err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}

		now := time.Now().UTC()
		sched.CreatedAt = now
		sched.Owner = owner
		nextRun := cronExpr.Next(now)
		sched.NextRun = &nextRun
	}

	source := sched.Source
	if source == "" {
		source = "manual"
	}

	var eventConfigJSON []byte
	if sched.EventConfig != nil {
		var err error
		eventConfigJSON, err = json.Marshal(sched.EventConfig)
		if err != nil {
			return fmt.Errorf("marshaling event config: %w", err)
		}
	}

	_, err := s.db.Exec(ctx, `
		INSERT INTO schedules (id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, next_run, created_at, owner, debug, source, source_key, trigger_type, event_config)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, sched.ID, sched.Name, sched.Cron, sched.Prompt, sched.Repo, sched.Provider,
		sched.ScopePreset, sched.Timeout, sched.Enabled, sched.NextRun, sched.CreatedAt, owner, sched.Debug,
		source, nilIfEmptySched(sched.SourceKey), triggerType, eventConfigJSON)
	if err != nil {
		return fmt.Errorf("inserting schedule: %w", err)
	}

	return nil
}

// ListSchedules returns schedules from the database.
// If owner is non-empty, only schedules belonging to that owner are returned.
func (s *Scheduler) ListSchedules(ctx context.Context, owner string) ([]Schedule, error) {
	query := `SELECT id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, last_run, next_run, created_at, owner, debug, source, source_key, trigger_type, event_config
		FROM schedules`
	args := []any{}
	if owner != "" {
		query += ` WHERE owner = $1`
		args = append(args, owner)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var sched Schedule
		var source, sourceKey, triggerType *string
		var eventConfigJSON []byte
		if err := rows.Scan(
			&sched.ID, &sched.Name, &sched.Cron, &sched.Prompt,
			&sched.Repo, &sched.Provider, &sched.ScopePreset, &sched.Timeout,
			&sched.Enabled, &sched.LastRun, &sched.NextRun, &sched.CreatedAt, &sched.Owner, &sched.Debug,
			&source, &sourceKey, &triggerType, &eventConfigJSON,
		); err != nil {
			return nil, fmt.Errorf("scanning schedule: %w", err)
		}
		if source != nil {
			sched.Source = *source
		}
		if sourceKey != nil {
			sched.SourceKey = *sourceKey
		}
		if triggerType != nil {
			sched.TriggerType = *triggerType
		}
		if eventConfigJSON != nil {
			var ec EventTrigger
			if json.Unmarshal(eventConfigJSON, &ec) == nil {
				sched.EventConfig = &ec
			}
		}
		schedules = append(schedules, sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating schedules: %w", err)
	}

	return schedules, nil
}

// GetSchedule retrieves a single schedule by ID.
// If owner is non-empty, only returns the schedule if it belongs to that owner.
func (s *Scheduler) GetSchedule(ctx context.Context, id, owner string) (*Schedule, error) {
	query := `SELECT id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, last_run, next_run, created_at, owner, debug, source, source_key, trigger_type, event_config
		FROM schedules WHERE id = $1`
	args := []any{id}
	if owner != "" {
		query += ` AND owner = $2`
		args = append(args, owner)
	}

	var sched Schedule
	var source, sourceKey, triggerType *string
	var eventConfigJSON []byte
	err := s.db.QueryRow(ctx, query, args...).Scan(
		&sched.ID, &sched.Name, &sched.Cron, &sched.Prompt,
		&sched.Repo, &sched.Provider, &sched.ScopePreset, &sched.Timeout,
		&sched.Enabled, &sched.LastRun, &sched.NextRun, &sched.CreatedAt, &sched.Owner, &sched.Debug,
		&source, &sourceKey, &triggerType, &eventConfigJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("querying schedule %s: %w", id, err)
	}
	if source != nil {
		sched.Source = *source
	}
	if sourceKey != nil {
		sched.SourceKey = *sourceKey
	}
	if triggerType != nil {
		sched.TriggerType = *triggerType
	}
	if eventConfigJSON != nil {
		var ec EventTrigger
		if json.Unmarshal(eventConfigJSON, &ec) == nil {
			sched.EventConfig = &ec
		}
	}
	return &sched, nil
}

// UpdateSchedule updates an existing schedule in the database. If the cron
// expression has changed, it recomputes next_run.
// If owner is non-empty, only updates the schedule if it belongs to that owner.
func (s *Scheduler) UpdateSchedule(ctx context.Context, sched *Schedule, owner string) error {
	triggerType := sched.TriggerType
	if triggerType == "" {
		triggerType = "cron"
	}

	// Event-only schedules don't need cron validation.
	if triggerType != "event" {
		cronExpr, err := ParseCron(sched.Cron)
		if err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
		now := time.Now().UTC()
		nextRun := cronExpr.Next(now)
		sched.NextRun = &nextRun
	}

	var eventConfigJSON []byte
	if sched.EventConfig != nil {
		var err error
		eventConfigJSON, err = json.Marshal(sched.EventConfig)
		if err != nil {
			return fmt.Errorf("marshaling event config: %w", err)
		}
	}

	query := `UPDATE schedules
		SET name = $1, cron = $2, prompt = $3, repo = $4, provider = $5,
		    scope_preset = $6, timeout = $7, enabled = $8, next_run = $9, debug = $10,
		    trigger_type = $11, event_config = $12
		WHERE id = $13`
	args := []any{sched.Name, sched.Cron, sched.Prompt, sched.Repo, sched.Provider,
		sched.ScopePreset, sched.Timeout, sched.Enabled, sched.NextRun, sched.Debug,
		triggerType, eventConfigJSON, sched.ID}
	if owner != "" {
		query += ` AND owner = $14`
		args = append(args, owner)
	}

	_, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating schedule: %w", err)
	}

	return nil
}

// DeleteSchedule removes a schedule from the database.
// If owner is non-empty, only deletes the schedule if it belongs to that owner.
func (s *Scheduler) DeleteSchedule(ctx context.Context, id, owner string) error {
	query := `DELETE FROM schedules WHERE id = $1`
	args := []any{id}
	if owner != "" {
		query += ` AND owner = $2`
		args = append(args, owner)
	}

	_, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("deleting schedule %s: %w", id, err)
	}
	return nil
}

// EnableSchedule sets the enabled flag for a schedule. When enabling, it
// recomputes next_run from the current time.
// If owner is non-empty, only updates the schedule if it belongs to that owner.
func (s *Scheduler) EnableSchedule(ctx context.Context, id string, enabled bool, owner string) error {
	if enabled {
		// Recompute next_run when enabling (only for cron-based schedules).
		sched, err := s.GetSchedule(ctx, id, owner)
		if err != nil {
			return err
		}

		// Event-only schedules don't need next_run.
		if sched.TriggerType == "event" {
			query := `UPDATE schedules SET enabled = $1 WHERE id = $2`
			args := []any{enabled, id}
			if owner != "" {
				query += ` AND owner = $3`
				args = append(args, owner)
			}
			_, err = s.db.Exec(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("enabling schedule %s: %w", id, err)
			}
			return nil
		}

		cronExpr, err := ParseCron(sched.Cron)
		if err != nil {
			return fmt.Errorf("invalid cron expression: %w", err)
		}
		now := time.Now().UTC()
		nextRun := cronExpr.Next(now)
		query := `UPDATE schedules SET enabled = $1, next_run = $2 WHERE id = $3`
		args := []any{enabled, nextRun, id}
		if owner != "" {
			query += ` AND owner = $4`
			args = append(args, owner)
		}
		_, err = s.db.Exec(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("enabling schedule %s: %w", id, err)
		}
		return nil
	}

	query := `UPDATE schedules SET enabled = $1 WHERE id = $2`
	args := []any{enabled, id}
	if owner != "" {
		query += ` AND owner = $3`
		args = append(args, owner)
	}
	_, err := s.db.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating schedule %s enabled state: %w", id, err)
	}
	return nil
}

// nilIfEmptySched returns nil if the string is empty, otherwise a pointer to it.
func nilIfEmptySched(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
