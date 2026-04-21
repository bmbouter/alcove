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
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/jackc/pgx/v5/pgxpool"
)

// githubEventTypeMap maps GitHub Events API type names to webhook event names.
var githubEventTypeMap = map[string]string{
	"CommitCommentEvent":            "commit_comment",
	"CreateEvent":                   "create",
	"DeleteEvent":                   "delete",
	"ForkEvent":                     "fork",
	"IssueCommentEvent":             "issue_comment",
	"IssuesEvent":                   "issues",
	"MemberEvent":                   "member",
	"PublicEvent":                   "public",
	"PullRequestEvent":              "pull_request",
	"PullRequestReviewEvent":        "pull_request_review",
	"PullRequestReviewCommentEvent": "pull_request_review_comment",
	"PushEvent":                     "push",
	"ReleaseEvent":                  "release",
	"WatchEvent":                    "watch",
}

// GitHubPoller polls the GitHub Events API for repos referenced by
// polling-mode event schedules and dispatches matching tasks.
type GitHubPoller struct {
	db             *pgxpool.Pool
	dispatcher     *Dispatcher
	credStore      *CredentialStore
	defStore       *AgentDefStore
	workflowEngine *WorkflowEngine
	client         *http.Client
}

// pollSchedule holds the fields needed from a schedule for polling and dispatch.
type pollSchedule struct {
	ID        string
	Name      string
	Prompt    string
	Repos     []internal.RepoSpec
	Provider  string
	Timeout   int
	TeamID    string
	Debug     bool
	SourceKey string
	Trigger   *GitHubTrigger
}

// PollAll queries all polling-mode event schedules, groups by repo, and polls each.
func (p *GitHubPoller) PollAll(ctx context.Context) {
	// Check system mode — skip polling when paused.
	var mode string
	_ = p.db.QueryRow(ctx, "SELECT value FROM system_state WHERE key = 'mode'").Scan(&mode)
	if mode == "paused" {
		return
	}

	rows, err := p.db.Query(ctx, `
		SELECT id, name, prompt, repos, provider, timeout, team_id, debug, event_config, COALESCE(source_key, '')
		FROM schedules
		WHERE enabled = true
		  AND COALESCE(trigger_type, 'cron') IN ('event', 'cron-and-event')
		  AND event_config IS NOT NULL
	`)
	if err != nil {
		log.Printf("poller: error querying schedules: %v", err)
		return
	}
	defer rows.Close()

	// Group schedules by target repo.
	type repoGroup struct {
		teamID    string
		schedules []pollSchedule
	}
	repoMap := make(map[string]*repoGroup)

	for rows.Next() {
		var ps pollSchedule
		var reposJSON []byte
		var eventConfigJSON []byte

		if err := rows.Scan(&ps.ID, &ps.Name, &ps.Prompt, &reposJSON,
			&ps.Provider, &ps.Timeout, &ps.TeamID, &ps.Debug, &eventConfigJSON, &ps.SourceKey); err != nil {
			log.Printf("poller: error scanning schedule: %v", err)
			continue
		}
		if reposJSON != nil {
			_ = json.Unmarshal(reposJSON, &ps.Repos)
		}

		var trigger EventTrigger
		if err := json.Unmarshal(eventConfigJSON, &trigger); err != nil {
			log.Printf("poller: error unmarshaling event_config for %s: %v", ps.Name, err)
			continue
		}
		if trigger.GitHub == nil {
			log.Printf("poller: schedule %s has no github trigger config", ps.Name)
			continue
		}

		// Skip webhook-only schedules.
		if strings.EqualFold(trigger.GitHub.DeliveryMode, "webhook") {
			continue
		}

		ps.Trigger = trigger.GitHub

		// Extract repos to poll from the trigger config.
		for _, repo := range trigger.GitHub.Repos {
			if repo == "*" || repo == "" {
				continue // Can't poll all of GitHub.
			}
			if _, ok := repoMap[repo]; !ok {
				repoMap[repo] = &repoGroup{teamID: ps.TeamID}
			}
			repoMap[repo].schedules = append(repoMap[repo].schedules, ps)
		}
	}

	if len(repoMap) == 0 {
		log.Printf("poller: no polling-mode event schedules found")
		return
	}
	for repo, group := range repoMap {
		p.pollRepo(ctx, repo, group.teamID, group.schedules)
	}
}

// pollRepo fetches events from a single GitHub repo and dispatches matching tasks.
func (p *GitHubPoller) pollRepo(ctx context.Context, repo, teamID string, schedules []pollSchedule) {
	// Load poll state (ETag for caching only).
	var etag string
	_ = p.db.QueryRow(ctx,
		`SELECT etag FROM github_poll_state WHERE repo = $1`, repo,
	).Scan(&etag)

	// Acquire GitHub token.
	token, apiHost, err := p.credStore.AcquireSCMTokenForOwner(ctx, "github", teamID)
	if err != nil {
		log.Printf("poller: no GitHub credential for %s (teamID %s): %v", repo, teamID, err)
		return
	}

	baseURL := "https://api.github.com"
	if apiHost != "" {
		baseURL = strings.TrimRight(apiHost, "/")
	}

	// Fetch events from all pages. We process ALL fetched events and rely
	// solely on the webhook_deliveries table for deduplication. No ID-based
	// skipping — GitHub event IDs are not chronologically ordered.
	type ghEvent struct {
		ID        string                `json:"id"`
		Type      string                `json:"type"`
		Repo      struct{ Name string } `json:"repo"`
		Payload   json.RawMessage       `json:"payload"`
		CreatedAt time.Time             `json:"created_at"`
	}

	var allEvents []ghEvent
	var newEtag string
	const maxPages = 10

	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/events?per_page=30&page=%d", baseURL, repo, page)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			log.Printf("poller: error creating request for %s: %v", repo, err)
			return
		}
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Accept", "application/vnd.github.v3+json")
		req.Header.Set("User-Agent", "alcove-poller")
		if page == 1 && etag != "" {
			req.Header.Set("If-None-Match", etag)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			log.Printf("poller: error fetching events for %s: %v", repo, err)
			return
		}

		if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
			if n, _ := strconv.Atoi(remaining); n < 100 {
				log.Printf("poller: GitHub rate limit low for %s: %s remaining", repo, remaining)
			}
		}

		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			_, _ = p.db.Exec(ctx,
				`INSERT INTO github_poll_state (repo, etag, last_event_id, last_polled_at) VALUES ($1, $2, '', NOW())
				ON CONFLICT (repo) DO UPDATE SET last_polled_at = NOW()`,
				repo, etag)
			return
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			log.Printf("poller: GitHub API returned %d for %s: %s", resp.StatusCode, repo, string(body))
			return
		}

		if page == 1 {
			newEtag = resp.Header.Get("ETag")
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			log.Printf("poller: error reading response for %s: %v", repo, err)
			return
		}

		var pageEvents []ghEvent
		if err := json.Unmarshal(body, &pageEvents); err != nil {
			log.Printf("poller: error parsing events for %s: %v", repo, err)
			return
		}

		if len(pageEvents) == 0 {
			break
		}

		allEvents = append(allEvents, pageEvents...)

		if len(pageEvents) < 30 {
			break // Last page.
		}
	}

	if len(allEvents) == 0 {
		if newEtag != "" {
			_, _ = p.db.Exec(ctx,
				`INSERT INTO github_poll_state (repo, etag, last_event_id, last_polled_at) VALUES ($1, $2, '', NOW())
				ON CONFLICT (repo) DO UPDATE SET etag = $2, last_polled_at = NOW()`,
				repo, newEtag)
		}
		return
	}

	log.Printf("poller: fetched %d events from %s across all pages", len(allEvents), repo)

	// Process events in chronological order.
	sort.Slice(allEvents, func(i, j int) bool {
		return allEvents[i].CreatedAt.Before(allEvents[j].CreatedAt)
	})

	dispatched := 0

	// Clean up old dedup entries (older than 5 minutes).
	_, _ = p.db.Exec(ctx, `DELETE FROM dispatched_dedup WHERE dispatched_at < NOW() - INTERVAL '5 minutes'`)

	// Track dispatched (issue_number, schedule_id) pairs to prevent duplicates in this poll cycle.
	dispatchedTasks := make(map[string]bool)

	for _, event := range allEvents {

		// Map GitHub API event type to webhook event name.
		eventType, ok := githubEventTypeMap[event.Type]
		if !ok {
			continue
		}

		// Extract action, branch, sha from payload.
		var payload map[string]interface{}
		_ = json.Unmarshal(event.Payload, &payload)

		action, _ := payload["action"].(string)
		branch := ""
		sha := ""
		prNumber := ""
		issueNumber := ""

		if ref, ok := payload["ref"].(string); ok {
			branch = strings.TrimPrefix(ref, "refs/heads/")
		}
		if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
			if head, ok := pr["head"].(map[string]interface{}); ok {
				if ref, ok := head["ref"].(string); ok {
					branch = ref
				}
				if s, ok := head["sha"].(string); ok {
					sha = s
				}
			}
			if num, ok := pr["number"].(float64); ok {
				prNumber = strconv.Itoa(int(num))
			}
		}

		// Extract issue number from issue events.
		if issue, ok := payload["issue"].(map[string]interface{}); ok {
			if num, ok := issue["number"].(float64); ok {
				issueNumber = strconv.Itoa(int(num))
			}
		}
		if commits, ok := payload["commits"].([]interface{}); ok && len(commits) > 0 {
			if last, ok := commits[len(commits)-1].(map[string]interface{}); ok {
				if s, ok := last["sha"].(string); ok {
					sha = s
				}
			}
		}

		// Extract issue/PR state from payload for filtering.
		var itemState string
		if issue, ok := payload["issue"].(map[string]interface{}); ok {
			if s, ok := issue["state"].(string); ok {
				itemState = s
			}
		} else if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
			if s, ok := pr["state"].(string); ok {
				itemState = s
			}
		}

		// Extract labels from issue or pull_request.
		var labels []string
		var addedLabel string
		if issue, ok := payload["issue"].(map[string]interface{}); ok {
			if labelArr, ok := issue["labels"].([]interface{}); ok {
				for _, lo := range labelArr {
					if lm, ok := lo.(map[string]interface{}); ok {
						if name, ok := lm["name"].(string); ok {
							labels = append(labels, name)
						}
					}
				}
			}
		} else if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
			if labelArr, ok := pr["labels"].([]interface{}); ok {
				for _, lo := range labelArr {
					if lm, ok := lo.(map[string]interface{}); ok {
						if name, ok := lm["name"].(string); ok {
							labels = append(labels, name)
						}
					}
				}
			}
		}
		// For "labeled" actions, the added label may not be in the
		// issue/PR labels array yet. Check payload["label"] directly.
		if action == "labeled" {
			if labelObj, ok := payload["label"].(map[string]interface{}); ok {
				if name, ok := labelObj["name"].(string); ok {
					addedLabel = name
					found := false
					for _, l := range labels {
						if strings.EqualFold(l, name) {
							found = true
							break
						}
					}
					if !found {
						labels = append(labels, name)
					}
				}
			}
		}

		// Extract user from comment, issue, or pull request.
		var users []string
		if comment, ok := payload["comment"].(map[string]interface{}); ok {
			if user, ok := comment["user"].(map[string]interface{}); ok {
				if login, ok := user["login"].(string); ok {
					users = append(users, login)
				}
			}
		} else if issue, ok := payload["issue"].(map[string]interface{}); ok {
			if user, ok := issue["user"].(map[string]interface{}); ok {
				if login, ok := user["login"].(string); ok {
					users = append(users, login)
				}
			}
		} else if pr, ok := payload["pull_request"].(map[string]interface{}); ok {
			if user, ok := pr["user"].(map[string]interface{}); ok {
				if login, ok := user["login"].(string); ok {
					users = append(users, login)
				}
			}
		}

		eventRepo := event.Repo.Name

		// Skip events for closed/merged issues and PRs. Only dispatch work
		// for items that are currently open. The event payload contains the
		// state at event time; we also verify current state via a live API
		// call to catch issues that were open when labeled but closed since.
		if itemState != "" && itemState != "open" {
			log.Printf("poller: skipping %s event for %s (item state: %s)", eventType, repo, itemState)
			continue
		}
		if issueNumber != "" && itemState == "open" {
			data, err := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/issues/%s", baseURL, repo, issueNumber))
			if err == nil {
				var current struct {
					State string `json:"state"`
				}
				if json.Unmarshal(data, &current) == nil && current.State != "open" {
					log.Printf("poller: skipping %s event for %s#%s (live state: %s)", eventType, repo, issueNumber, current.State)
					continue
				}
			}
		}

		// Match against each schedule.
		for _, sched := range schedules {
			if !sched.Trigger.Matches(eventType, action, eventRepo, branch, labels, users) {
				continue
			}

			// Deduplicate within this poll cycle to prevent multiple dispatches
			// for the same issue/PR + schedule combination.
			dedupeKey := ""
			if issueNumber != "" {
				dedupeKey = fmt.Sprintf("issue:%s:schedule:%s", issueNumber, sched.ID)
			} else if prNumber != "" {
				dedupeKey = fmt.Sprintf("pr:%s:schedule:%s", prNumber, sched.ID)
			}
			if dedupeKey != "" && dispatchedTasks[dedupeKey] {
				continue // Already dispatched this task for this issue/PR in this poll cycle.
			}

			// Deduplicate via webhook_deliveries (per-event dedup).
			deliveryID := fmt.Sprintf("poll-%s", event.ID)
			result, _ := p.db.Exec(ctx,
				`INSERT INTO webhook_deliveries (delivery_id, event_type, repo, action, received_at)
				VALUES ($1, $2, $3, $4, NOW())
				ON CONFLICT DO NOTHING`,
				deliveryID, eventType, eventRepo, action)
			if result.RowsAffected() == 0 {
				continue // Already processed this event.
			}

			// Persistent dedup: prevent dispatching the same schedule for the
			// same issue/PR across poll cycles. GitHub emits multiple events
			// for a single action (e.g., "opened" + "labeled"), and they may
			// arrive in different poll cycles. This check runs AFTER
			// webhook_deliveries so we don't consume dedup slots on events
			// that were already processed.
			itemNumber := issueNumber
			if itemNumber == "" {
				itemNumber = prNumber
			}
			if itemNumber != "" {
				dedupResult, _ := p.db.Exec(ctx,
					`INSERT INTO dispatched_dedup (repo, item_number, schedule_id)
					VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`,
					eventRepo, itemNumber, sched.ID)
				if dedupResult.RowsAffected() == 0 {
					continue // Already dispatched for this issue/PR + schedule recently.
				}
			}

			// Build session request. Look up agent definition for profiles.
			taskReq := TaskRequest{
				Prompt:   sched.Prompt,
				Repos:    sched.Repos,
				Provider: sched.Provider,
				Timeout:  sched.Timeout,
				Debug:    sched.Debug,
			}
			if sched.SourceKey != "" {
				var parsedJSON []byte
				_ = p.db.QueryRow(ctx,
					`SELECT parsed FROM agent_definitions WHERE source_key = $1`, sched.SourceKey,
				).Scan(&parsedJSON)
				if parsedJSON != nil {
					var td AgentDefinition
					if json.Unmarshal(parsedJSON, &td) == nil && len(td.Profiles) > 0 {
						taskReq.Profiles = td.Profiles
					}
				}
			}

			// Append event context to the prompt before dispatching.
			meta := map[string]string{
				"GITHUB_EVENT":        eventType,
				"GITHUB_REPO":         eventRepo,
				"GITHUB_REF":          branch,
				"GITHUB_SHA":          sha,
				"GITHUB_PR_NUMBER":    prNumber,
				"GITHUB_ISSUE_NUMBER": issueNumber,
				"GITHUB_LABEL_ADDED":  addedLabel,
			}

			// Enrich event context with full GitHub data.
			enrichedContext := p.enrichEventContext(ctx, token, baseURL, eventType, action, meta)

			// Set task metadata for session storage.
			taskReq.TaskName = sched.Name
			taskReq.TriggerType = "event"
			if issueNumber != "" {
				taskReq.TriggerRef = fmt.Sprintf("%s#%s", eventRepo, issueNumber)
			} else if prNumber != "" {
				taskReq.TriggerRef = fmt.Sprintf("%s#%s", eventRepo, prNumber)
			}

			// Prepend enriched context, keep raw metadata for backward compatibility.
			metaJSON, _ := json.Marshal(meta)
			taskReq.Prompt = taskReq.Prompt + "\n\n" + enrichedContext + "\n\n[event: " + string(metaJSON) + "]"

			// Route workflow schedules through the workflow engine.
			if sched.Provider == "workflow" && p.workflowEngine != nil && sched.SourceKey != "" {
				// Look up the workflow by source_key to get its ID.
				var workflowID string
				err := p.db.QueryRow(ctx,
					`SELECT id FROM workflows WHERE source_key = $1 AND team_id = $2`,
					sched.SourceKey, sched.TeamID,
				).Scan(&workflowID)
				if err != nil {
					log.Printf("poller: error looking up workflow for schedule %s: %v", sched.Name, err)
					continue
				}
				// Extract issue context for workflow trigger context
				var triggerContext map[string]interface{}
				if issueNumber != "" {
					triggerContext = p.extractIssueContext(ctx, token, baseURL, eventRepo, issueNumber, event.Payload)
				}

				_, err = p.workflowEngine.StartWorkflowRun(ctx, workflowID, "event", taskReq.TriggerRef, sched.TeamID, triggerContext)
				if err != nil {
					log.Printf("poller: error starting workflow run for %s: %v", sched.Name, err)
					continue
				}
			} else {
				_, err := p.dispatcher.DispatchTask(ctx, taskReq, "poller", sched.TeamID)
				if err != nil {
					log.Printf("poller: error dispatching schedule %s for %s: %v", sched.Name, eventRepo, err)
					continue
				}
			}

			// Mark this task as dispatched for this issue/PR in this poll cycle.
			if dedupeKey != "" {
				dispatchedTasks[dedupeKey] = true
			}

			dispatched++
			log.Printf("poller: dispatched %s for %s %s/%s (event %s)", sched.Name, eventType, eventRepo, action, event.ID)
		}
	}

	// Update poll state (ETag only — deduplication is via webhook_deliveries).
	_, _ = p.db.Exec(ctx,
		`INSERT INTO github_poll_state (repo, etag, last_event_id, last_polled_at) VALUES ($1, $2, '', NOW())
		ON CONFLICT (repo) DO UPDATE SET etag = $2, last_polled_at = NOW()`,
		repo, newEtag)

	if dispatched > 0 {
		log.Printf("poller: dispatched %d task(s) from %s", dispatched, repo)
	}
}

// extractIssueContext extracts issue context (title, body, url) from the GitHub event payload.
// This context is used for workflow template expansion of trigger variables.
func (p *GitHubPoller) extractIssueContext(ctx context.Context, token, baseURL, repo, issueNumber string, payload json.RawMessage) map[string]interface{} {
	context := make(map[string]interface{})

	// First try to extract from the event payload itself
	var eventPayload map[string]interface{}
	if json.Unmarshal(payload, &eventPayload) == nil {
		if issue, ok := eventPayload["issue"].(map[string]interface{}); ok {
			if title, ok := issue["title"].(string); ok {
				context["issue_title"] = title
			}
			if body, ok := issue["body"].(string); ok {
				context["issue_body"] = body
			}
			if htmlURL, ok := issue["html_url"].(string); ok {
				context["issue_url"] = htmlURL
			}
		}
	}

	// If we have the basic info from payload, we're good
	if _, hasTitle := context["issue_title"]; hasTitle {
		return context
	}

	// Otherwise, fetch from GitHub API as fallback
	data, err := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/issues/%s", baseURL, repo, issueNumber))
	if err != nil {
		log.Printf("poller: context: could not fetch issue #%s: %v", issueNumber, err)
		return context
	}

	var issue struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		log.Printf("poller: context: error parsing issue #%s: %v", issueNumber, err)
		return context
	}

	context["issue_title"] = issue.Title
	context["issue_body"] = issue.Body
	context["issue_url"] = issue.HTMLURL

	return context
}
