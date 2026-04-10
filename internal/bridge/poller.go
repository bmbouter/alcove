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
	db         *pgxpool.Pool
	dispatcher *Dispatcher
	credStore  *CredentialStore
	defStore   *TaskDefStore
	client     *http.Client
}

// pollSchedule holds the fields needed from a schedule for polling and dispatch.
type pollSchedule struct {
	ID          string
	Name        string
	Prompt      string
	Repo        string
	Provider    string
	Timeout     int
	Owner       string
	Debug       bool
	SourceKey   string
	Trigger     *GitHubTrigger
}

// PollAll queries all polling-mode event schedules, groups by repo, and polls each.
func (p *GitHubPoller) PollAll(ctx context.Context) {
	rows, err := p.db.Query(ctx, `
		SELECT id, name, prompt, repo, provider, timeout, owner, debug, event_config, COALESCE(source_key, '')
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
		owner     string
		schedules []pollSchedule
	}
	repoMap := make(map[string]*repoGroup)

	for rows.Next() {
		var ps pollSchedule
		var eventConfigJSON []byte

		if err := rows.Scan(&ps.ID, &ps.Name, &ps.Prompt, &ps.Repo,
			&ps.Provider, &ps.Timeout, &ps.Owner, &ps.Debug, &eventConfigJSON, &ps.SourceKey); err != nil {
			log.Printf("poller: error scanning schedule: %v", err)
			continue
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
				repoMap[repo] = &repoGroup{owner: ps.Owner}
			}
			repoMap[repo].schedules = append(repoMap[repo].schedules, ps)
		}
	}

	if len(repoMap) == 0 {
		log.Printf("poller: no polling-mode event schedules found")
		return
	}
	for repo, group := range repoMap {
		p.pollRepo(ctx, repo, group.owner, group.schedules)
	}
}

// pollRepo fetches events from a single GitHub repo and dispatches matching tasks.
func (p *GitHubPoller) pollRepo(ctx context.Context, repo, owner string, schedules []pollSchedule) {
	// Load poll state (ETag for caching only).
	var etag string
	_ = p.db.QueryRow(ctx,
		`SELECT etag FROM github_poll_state WHERE repo = $1`, repo,
	).Scan(&etag)

	// Acquire GitHub token.
	token, apiHost, err := p.credStore.AcquireSCMTokenForOwner(ctx, "github", owner)
	if err != nil {
		log.Printf("poller: no GitHub credential for %s (owner %s): %v", repo, owner, err)
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
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Repo      struct{ Name string } `json:"repo"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt time.Time       `json:"created_at"`
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
		// Extract issue number for issue events
		if issue, ok := payload["issue"].(map[string]interface{}); ok {
			if num, ok := issue["number"].(float64); ok {
				issueNumber = strconv.Itoa(int(num))
			}
		}

		// Extract labels from issue or pull_request.
		var labels []string
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

		// Extract user from comment or issue.
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
		}

		eventRepo := event.Repo.Name

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

			// Deduplicate via webhook_deliveries.
			deliveryID := fmt.Sprintf("poll-%s", event.ID)
			result, _ := p.db.Exec(ctx,
				`INSERT INTO webhook_deliveries (delivery_id, event_type, repo, action, received_at)
				VALUES ($1, $2, $3, $4, NOW())
				ON CONFLICT DO NOTHING`,
				deliveryID, eventType, eventRepo, action)
			if result.RowsAffected() == 0 {
				continue // Already processed.
			}

			// Build task request. Look up task definition for profiles.
			taskReq := TaskRequest{
				Prompt:   sched.Prompt,
				Repo:     sched.Repo,
				Provider: sched.Provider,
				Timeout:  sched.Timeout,
				Debug:    sched.Debug,
			}
			if sched.SourceKey != "" {
				var parsedJSON []byte
				_ = p.db.QueryRow(ctx,
					`SELECT parsed FROM task_definitions WHERE source_key = $1`, sched.SourceKey,
				).Scan(&parsedJSON)
				if parsedJSON != nil {
					var td TaskDefinition
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
			}
			metaJSON, _ := json.Marshal(meta)
			taskReq.Prompt = taskReq.Prompt + "\n\n[event: " + string(metaJSON) + "]"

			_, err := p.dispatcher.DispatchTask(ctx, taskReq, sched.Owner)
			if err != nil {
				log.Printf("poller: error dispatching schedule %s for %s: %v", sched.Name, eventRepo, err)
				continue
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
