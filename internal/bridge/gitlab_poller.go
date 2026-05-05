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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alcove-ai/alcove/internal"
	"github.com/jackc/pgx/v5/pgxpool"
)

// gitlabEvent represents an event from the GitLab Events API.
type gitlabEvent struct {
	ID           int64       `json:"id"`
	TargetType   string      `json:"target_type"`
	ActionName   string      `json:"action_name"`
	ProjectID    int64       `json:"project_id"`
	TargetTitle  string      `json:"target_title"`
	CreatedAt    time.Time   `json:"created_at"`
	PushData     interface{} `json:"push_data"`
	TargetIID    *int        `json:"target_iid"`    // Issue/MR internal ID
	TargetID     *int64      `json:"target_id"`     // Issue/MR global ID
}

// gitlabEventTypeMap maps GitLab Events API target_type to normalized event names.
var gitlabEventTypeMap = map[string]string{
	"MergeRequest": "merge_request",
	"Issue":        "issue",
	"Note":         "comment",
	"PushEvent":    "push",
	"Milestone":    "milestone",
}

// GitLabPoller polls GitLab Events API for recently updated projects and triggers workflows
// whose definitions include a gitlab trigger that matches.
type GitLabPoller struct {
	db             *pgxpool.Pool
	credStore      *CredentialStore
	workflowEngine *WorkflowEngine
	defStore       *AgentDefStore
	dispatcher     *Dispatcher
	enricher       *GitLabEnricher
	pollInterval   time.Duration
	lastPollTime   time.Time
}

// NewGitLabPoller creates a GitLabPoller with the given dependencies.
func NewGitLabPoller(db *pgxpool.Pool, credStore *CredentialStore, we *WorkflowEngine, defStore *AgentDefStore, dispatcher *Dispatcher) *GitLabPoller {
	return &GitLabPoller{
		db:             db,
		credStore:      credStore,
		workflowEngine: we,
		defStore:       defStore,
		dispatcher:     dispatcher,
		enricher:       NewGitLabEnricher(nil),
		pollInterval:   2 * time.Minute,
		lastPollTime:   time.Now().Add(-5 * time.Minute),
	}
}

// Start begins the GitLab polling loop in the current goroutine. It blocks until
// the context is cancelled.
func (gp *GitLabPoller) Start(ctx context.Context) {
	ticker := time.NewTicker(gp.pollInterval)
	defer ticker.Stop()

	// Initial poll after 30 seconds
	time.Sleep(30 * time.Second)
	gp.PollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gp.PollAll(ctx)
		}
	}
}

// gitlabPollTarget holds the information needed to match and dispatch a GitLab-
// triggered workflow or schedule.
type gitlabPollTarget struct {
	workflowID  string // For workflows
	scheduleID  string // For schedules
	teamID      string
	trigger     *GitLabTrigger
	isWorkflow  bool // true = workflow, false = schedule
	prompt      string
	repos       []string // For schedules
	provider    string
	timeout     int
	debug       bool
	sourceKey   string
}

// PollAll queries both workflows and schedules with GitLab triggers and polls GitLab
// for matching recently-updated projects.
func (gp *GitLabPoller) PollAll(ctx context.Context) {
	// Check system mode — skip polling when paused.
	var mode string
	_ = gp.db.QueryRow(ctx, "SELECT value FROM system_state WHERE key = 'mode'").Scan(&mode)
	if mode == "paused" {
		return
	}

	// Query workflows with GitLab triggers
	workflowTargets := gp.queryWorkflowTargets(ctx)

	// Query schedules with GitLab triggers
	scheduleTargets := gp.queryScheduleTargets(ctx)

	// Combine all targets
	allTargets := append(workflowTargets, scheduleTargets...)

	if len(allTargets) == 0 {
		return
	}

	// Group by team to minimize credential lookups.
	teamTargets := make(map[string][]gitlabPollTarget)
	for _, t := range allTargets {
		teamTargets[t.teamID] = append(teamTargets[t.teamID], t)
	}

	for teamID, tgts := range teamTargets {
		gp.pollForTeam(ctx, teamID, tgts)
	}

	gp.lastPollTime = time.Now()
}

// queryWorkflowTargets queries all workflows with GitLab triggers.
func (gp *GitLabPoller) queryWorkflowTargets(ctx context.Context) []gitlabPollTarget {
	rows, err := gp.db.Query(ctx, `
		SELECT w.id, w.name, w.parsed, w.team_id
		FROM workflows w
		WHERE w.parsed IS NOT NULL
	`)
	if err != nil {
		log.Printf("gitlab-poller: error querying workflows: %v", err)
		return nil
	}
	defer rows.Close()

	var targets []gitlabPollTarget

	for rows.Next() {
		var wfID, wfName, teamID string
		var parsedJSON []byte
		if err := rows.Scan(&wfID, &wfName, &parsedJSON, &teamID); err != nil {
			continue
		}

		var wd WorkflowDefinition
		if err := json.Unmarshal(parsedJSON, &wd); err != nil {
			continue
		}

		if wd.Trigger != nil && wd.Trigger.GitLab != nil {
			targets = append(targets, gitlabPollTarget{
				workflowID: wfID,
				teamID:     teamID,
				trigger:    wd.Trigger.GitLab,
				isWorkflow: true,
			})
		}
	}

	return targets
}

// queryScheduleTargets queries all schedules with GitLab triggers.
func (gp *GitLabPoller) queryScheduleTargets(ctx context.Context) []gitlabPollTarget {
	rows, err := gp.db.Query(ctx, `
		SELECT id, name, prompt, repos, provider, timeout, team_id, debug, event_config, COALESCE(source_key, '')
		FROM schedules
		WHERE enabled = true
		  AND COALESCE(trigger_type, 'cron') IN ('event', 'cron-and-event')
		  AND event_config IS NOT NULL
		  AND event_config::jsonb ? 'gitlab'
	`)
	if err != nil {
		log.Printf("gitlab-poller: error querying schedules: %v", err)
		return nil
	}
	defer rows.Close()

	var targets []gitlabPollTarget

	for rows.Next() {
		var schedID, name, prompt, provider, sourceKey, teamID string
		var reposJSON, eventConfigJSON []byte
		var timeout int
		var debug bool

		if err := rows.Scan(&schedID, &name, &prompt, &reposJSON, &provider, &timeout, &teamID, &debug, &eventConfigJSON, &sourceKey); err != nil {
			log.Printf("gitlab-poller: error scanning schedule: %v", err)
			continue
		}

		var trigger EventTrigger
		if err := json.Unmarshal(eventConfigJSON, &trigger); err != nil {
			log.Printf("gitlab-poller: error unmarshaling event_config for %s: %v", name, err)
			continue
		}
		if trigger.GitLab == nil {
			continue // Should not happen due to query filter, but be safe
		}

		var repos []string
		if reposJSON != nil {
			// This could be either a simple repo list or RepoSpec objects
			var repoSpecs []map[string]interface{}
			if err := json.Unmarshal(reposJSON, &repoSpecs); err == nil {
				// Try as RepoSpec objects
				for _, spec := range repoSpecs {
					if url, ok := spec["url"].(string); ok {
						repos = append(repos, url)
					}
				}
			} else {
				// Try as simple string list
				_ = json.Unmarshal(reposJSON, &repos)
			}
		}

		targets = append(targets, gitlabPollTarget{
			scheduleID: schedID,
			teamID:     teamID,
			trigger:    trigger.GitLab,
			isWorkflow: false,
			prompt:     prompt,
			repos:      repos,
			provider:   provider,
			timeout:    timeout,
			debug:      debug,
			sourceKey:  sourceKey,
		})
	}

	return targets
}

func (gp *GitLabPoller) pollForTeam(ctx context.Context, teamID string, targets []gitlabPollTarget) {
	token, apiHost, err := gp.credStore.AcquireSCMTokenForOwner(ctx, "gitlab", teamID)
	if err != nil {
		log.Printf("gitlab-poller: no gitlab credential for team %s: %v", teamID, err)
		return
	}

	// Default to gitlab.com, not Red Hat internal instance
	if apiHost == "" {
		apiHost = "https://gitlab.com"
	}

	// Collect all projects from targets.
	projectSet := make(map[string]bool)
	for _, t := range targets {
		for _, p := range t.trigger.Projects {
			if p != "" && p != "*" { // Can't poll all of GitLab
				projectSet[p] = true
			}
		}
		// For schedules, extract project paths from repo URLs if Projects is empty
		if len(t.trigger.Projects) == 0 && !t.isWorkflow {
			for _, repo := range t.repos {
				if project := gp.extractProjectFromRepoURL(repo); project != "" {
					projectSet[project] = true
				}
			}
		}
	}

	if len(projectSet) == 0 {
		log.Printf("gitlab-poller: no specific projects to poll for team %s", teamID)
		return
	}

	// Poll each project
	for project := range projectSet {
		gp.pollProject(ctx, token, apiHost, project, targets)
	}
}

// extractProjectFromRepoURL extracts the GitLab project path from a repository URL.
// Examples:
//   https://gitlab.com/group/project.git -> group/project
//   git@gitlab.com:group/project.git -> group/project
func (gp *GitLabPoller) extractProjectFromRepoURL(repoURL string) string {
	if repoURL == "" {
		return ""
	}

	// Remove .git suffix
	repoURL = strings.TrimSuffix(repoURL, ".git")

	// Handle HTTPS URLs
	if strings.HasPrefix(repoURL, "https://gitlab.com/") {
		return strings.TrimPrefix(repoURL, "https://gitlab.com/")
	}
	if strings.Contains(repoURL, "gitlab") && strings.HasPrefix(repoURL, "https://") {
		// For self-hosted instances like https://gitlab.example.com/group/project
		parts := strings.Split(repoURL, "/")
		if len(parts) >= 5 { // https, "", hostname, group, project...
			return strings.Join(parts[3:], "/")
		}
	}

	// Handle SSH URLs
	if strings.HasPrefix(repoURL, "git@gitlab.com:") {
		return strings.TrimPrefix(repoURL, "git@gitlab.com:")
	}
	if strings.Contains(repoURL, "git@") && strings.Contains(repoURL, "gitlab") {
		// For SSH URLs like git@gitlab.example.com:group/project
		parts := strings.Split(repoURL, ":")
		if len(parts) >= 2 {
			return parts[1]
		}
	}

	return ""
}

func (gp *GitLabPoller) pollProject(ctx context.Context, token, apiHost, projectPath string, targets []gitlabPollTarget) {
	// URL-encode the project path for the API URL (GitLab requires this)
	encodedProject := strings.ReplaceAll(projectPath, "/", "%2F")

	// Load poll state (last event ID for incremental polling)
	var lastEventID int64
	_ = gp.db.QueryRow(ctx,
		`SELECT last_event_id FROM gitlab_poll_state WHERE project = $1`, encodedProject,
	).Scan(&lastEventID)

	// Fetch events from GitLab Events API
	eventsURL := fmt.Sprintf("%s/api/v4/projects/%s/events?per_page=100", apiHost, encodedProject)

	respBody, err := gitlabRequest(ctx, token, "GET", eventsURL, nil)
	if err != nil {
		log.Printf("gitlab-poller: error fetching events for %s: %v", projectPath, err)
		return
	}

	// Parse events response
	var events []gitlabEvent
	if err := json.Unmarshal(respBody, &events); err != nil {
		log.Printf("gitlab-poller: error parsing events for %s: %v", projectPath, err)
		return
	}

	if len(events) == 0 {
		// Update poll state even if no events
		_, _ = gp.db.Exec(ctx,
			`INSERT INTO gitlab_poll_state (project, last_event_id, last_polled_at) VALUES ($1, $2, NOW())
			ON CONFLICT (project) DO UPDATE SET last_polled_at = NOW()`,
			encodedProject, lastEventID)
		return
	}

	log.Printf("gitlab-poller: fetched %d events from %s", len(events), projectPath)

	// Process events in chronological order, filter out events older than lastEventID
	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})

	var newEvents []gitlabEvent
	var maxEventID int64 = lastEventID

	for _, event := range events {
		if event.ID > lastEventID {
			newEvents = append(newEvents, event)
		}
		if event.ID > maxEventID {
			maxEventID = event.ID
		}
	}

	if len(newEvents) == 0 {
		log.Printf("gitlab-poller: no new events for %s (last ID: %d)", projectPath, lastEventID)
		// Update poll state
		_, _ = gp.db.Exec(ctx,
			`INSERT INTO gitlab_poll_state (project, last_event_id, last_polled_at) VALUES ($1, $2, NOW())
			ON CONFLICT (project) DO UPDATE SET last_polled_at = NOW()`,
			encodedProject, maxEventID)
		return
	}

	log.Printf("gitlab-poller: processing %d new events for %s", len(newEvents), projectPath)

	dispatched := 0

	// Clean up old dedup entries (older than 5 minutes).
	_, _ = gp.db.Exec(ctx, `DELETE FROM dispatched_dedup WHERE dispatched_at < NOW() - INTERVAL '5 minutes'`)

	// Track dispatched (item_number, schedule_id/workflow_id) pairs to prevent duplicates in this poll cycle.
	dispatchedTasks := make(map[string]bool)

	for _, event := range newEvents {
		// Map GitLab target_type to normalized event type
		eventType, ok := gitlabEventTypeMap[event.TargetType]
		if !ok {
			// Unknown event type, skip
			continue
		}

		action := event.ActionName // GitLab actions pass through directly
		branch := ""
		itemNumber := ""

		// Extract branch from push events
		if event.TargetType == "PushEvent" && event.PushData != nil {
			if pushMap, ok := event.PushData.(map[string]interface{}); ok {
				if ref, ok := pushMap["ref"].(string); ok {
					branch = strings.TrimPrefix(ref, "refs/heads/")
				}
			}
		}

		// Extract issue/MR number
		if event.TargetIID != nil {
			itemNumber = strconv.Itoa(*event.TargetIID)
		}

		// For now, labels extraction requires a separate API call to get full issue/MR details
		// We'll implement basic matching without labels initially, then add enrichment
		var labels []string

		// Match against each target
		for _, target := range targets {
			if !target.trigger.Matches(eventType, action, projectPath, branch, labels) {
				continue
			}

			// Deduplicate within this poll cycle
			dedupeKey := ""
			if itemNumber != "" {
				if target.isWorkflow {
					dedupeKey = fmt.Sprintf("item:%s:workflow:%s", itemNumber, target.workflowID)
				} else {
					dedupeKey = fmt.Sprintf("item:%s:schedule:%s", itemNumber, target.scheduleID)
				}
			}
			if dedupeKey != "" && dispatchedTasks[dedupeKey] {
				continue // Already dispatched this task for this item in this poll cycle.
			}

			// Deduplicate via webhook_deliveries (per-event dedup).
			deliveryID := fmt.Sprintf("gitlab-poll-%d", event.ID)
			result, _ := gp.db.Exec(ctx,
				`INSERT INTO webhook_deliveries (delivery_id, event_type, repo, action, received_at)
				VALUES ($1, $2, $3, $4, NOW())
				ON CONFLICT DO NOTHING`,
				deliveryID, eventType, projectPath, action)
			if result.RowsAffected() == 0 {
				continue // Already processed this event.
			}

			// Persistent dedup: prevent dispatching the same target for the same item across poll cycles.
			if itemNumber != "" {
				var targetID string
				if target.isWorkflow {
					targetID = target.workflowID
				} else {
					targetID = target.scheduleID
				}

				dedupResult, _ := gp.db.Exec(ctx,
					`INSERT INTO dispatched_dedup (repo, item_number, schedule_id)
					VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`,
					projectPath, itemNumber, targetID)
				if dedupResult.RowsAffected() == 0 {
					continue // Already dispatched for this item + target recently.
				}
			}

			// Extract enriched context for the event using the GitLabEnricher
			meta := map[string]string{
				"GITLAB_PROJECT": projectPath,
			}
			if itemNumber != "" {
				if eventType == "issue" {
					meta["GITLAB_ISSUE_IID"] = itemNumber
				} else if eventType == "merge_request" {
					meta["GITLAB_MR_IID"] = itemNumber
				}
			}
			enrichedContext := gp.enricher.EnrichGitLabEventContext(ctx, token, apiHost, eventType, action, meta)

			if target.isWorkflow {
				// Dispatch workflow
				triggerRef := fmt.Sprintf("%s#%s", projectPath, itemNumber)
				if itemNumber == "" {
					triggerRef = projectPath
				}

				triggerContext := map[string]interface{}{
					"event_type":       eventType,
					"action":           action,
					"project":          projectPath,
					"branch":           branch,
					"enriched_context": enrichedContext,
				}

				// Add event-specific context
				if itemNumber != "" {
					if eventType == "issue" {
						triggerContext["issue_iid"] = itemNumber
					} else if eventType == "merge_request" {
						triggerContext["mr_iid"] = itemNumber
					}
				}

				_, err := gp.workflowEngine.StartWorkflowRun(ctx, target.workflowID, "event", triggerRef, target.teamID, triggerContext)
				if err != nil {
					log.Printf("gitlab-poller: error starting workflow for %s: %v", triggerRef, err)
					continue
				}

				log.Printf("gitlab-poller: triggered workflow %s for %s %s/%s (event %d)", target.workflowID, eventType, projectPath, action, event.ID)

			} else {
				// Dispatch schedule (agent task)
				taskReq := TaskRequest{
					Prompt:      target.prompt,
					Repos:       convertRepoStringsToSpecs(target.repos),
					Provider:    target.provider,
					Timeout:     target.timeout,
					Debug:       target.debug,
					TaskName:    fmt.Sprintf("gitlab-%s", target.scheduleID),
					TriggerType: "event",
				}

				if itemNumber != "" {
					taskReq.TriggerRef = fmt.Sprintf("%s#%s", projectPath, itemNumber)
				} else {
					taskReq.TriggerRef = projectPath
				}

				// Set metadata env vars
				meta := map[string]string{
					"GITLAB_EVENT":   eventType,
					"GITLAB_PROJECT": projectPath,
					"GITLAB_REF":     branch,
				}
				if itemNumber != "" {
					if eventType == "issue" {
						meta["GITLAB_ISSUE_IID"] = itemNumber
					} else if eventType == "merge_request" {
						meta["GITLAB_MR_IID"] = itemNumber
					}
				}

				// Prepend enriched context to prompt
				metaJSON, _ := json.Marshal(meta)
				taskReq.Prompt = taskReq.Prompt + "\n\n" + enrichedContext + "\n\n[event: " + string(metaJSON) + "]"

				// Look up agent profiles if available
				if target.sourceKey != "" {
					var parsedJSON []byte
					_ = gp.db.QueryRow(ctx,
						`SELECT parsed FROM agent_definitions WHERE source_key = $1`, target.sourceKey,
					).Scan(&parsedJSON)
					if parsedJSON != nil {
						var td AgentDefinition
						if json.Unmarshal(parsedJSON, &td) == nil && len(td.Profiles) > 0 {
							taskReq.Profiles = td.Profiles
						}
					}
				}

				_, err := gp.dispatcher.DispatchTask(ctx, taskReq, "gitlab-poller", target.teamID)
				if err != nil {
					log.Printf("gitlab-poller: error dispatching schedule %s for %s: %v", target.scheduleID, projectPath, err)
					continue
				}

				log.Printf("gitlab-poller: dispatched schedule %s for %s %s/%s (event %d)", target.scheduleID, eventType, projectPath, action, event.ID)
			}

			// Mark this task as dispatched for this item in this poll cycle.
			if dedupeKey != "" {
				dispatchedTasks[dedupeKey] = true
			}

			dispatched++
		}
	}

	// Update poll state with the highest event ID seen
	_, _ = gp.db.Exec(ctx,
		`INSERT INTO gitlab_poll_state (project, last_event_id, last_polled_at) VALUES ($1, $2, NOW())
		ON CONFLICT (project) DO UPDATE SET last_event_id = $2, last_polled_at = NOW()`,
		encodedProject, maxEventID)

	if dispatched > 0 {
		log.Printf("gitlab-poller: dispatched %d task(s) from %s", dispatched, projectPath)
	}
}

// convertRepoStringsToSpecs converts a slice of repo URLs to RepoSpec objects.
func convertRepoStringsToSpecs(repos []string) []internal.RepoSpec {
	var specs []internal.RepoSpec
	for _, repo := range repos {
		specs = append(specs, internal.RepoSpec{
			Name: extractRepoNameFromURL(repo),
			URL:  repo,
			Ref:  "", // Use default branch
		})
	}
	return specs
}

// extractRepoNameFromURL extracts a repo name from a git URL.
func extractRepoNameFromURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "repo"
}