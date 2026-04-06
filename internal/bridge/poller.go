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
		return
	}
	for repo, group := range repoMap {
		p.pollRepo(ctx, repo, group.owner, group.schedules)
	}
}

// pollRepo fetches events from a single GitHub repo and dispatches matching tasks.
func (p *GitHubPoller) pollRepo(ctx context.Context, repo, owner string, schedules []pollSchedule) {
	// Load poll state.
	var etag, lastEventID string
	_ = p.db.QueryRow(ctx,
		`SELECT etag, last_event_id FROM github_poll_state WHERE repo = $1`, repo,
	).Scan(&etag, &lastEventID)

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
	url := fmt.Sprintf("%s/repos/%s/events?per_page=30", baseURL, repo)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("poller: error creating request for %s: %v", repo, err)
		return
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "alcove-poller")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		log.Printf("poller: error fetching events for %s: %v", repo, err)
		return
	}
	defer resp.Body.Close()

	// Log rate limit info.
	if remaining := resp.Header.Get("X-RateLimit-Remaining"); remaining != "" {
		if n, _ := strconv.Atoi(remaining); n < 100 {
			log.Printf("poller: GitHub rate limit low for %s: %s remaining", repo, remaining)
		}
	}

	if resp.StatusCode == http.StatusNotModified {
		// No new events.
		_, _ = p.db.Exec(ctx,
			`INSERT INTO github_poll_state (repo, etag, last_event_id, last_polled_at) VALUES ($1, $2, $3, NOW())
			ON CONFLICT (repo) DO UPDATE SET last_polled_at = NOW()`,
			repo, etag, lastEventID)
		return
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("poller: GitHub API returned %d for %s: %s", resp.StatusCode, repo, string(body))
		return
	}

	// Parse events.
	var events []struct {
		ID        string          `json:"id"`
		Type      string          `json:"type"`
		Repo      struct{ Name string } `json:"repo"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt time.Time       `json:"created_at"`
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		log.Printf("poller: error reading response for %s: %v", repo, err)
		return
	}
	if err := json.Unmarshal(body, &events); err != nil {
		log.Printf("poller: error parsing events for %s: %v", repo, err)
		return
	}

	newEtag := resp.Header.Get("ETag")

	// First poll: process events from the last hour to catch recent activity.
	firstPoll := lastEventID == ""
	cutoff := time.Now().Add(-1 * time.Hour)

	// Reverse events to process in chronological order.
	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})

	var highestID string
	dispatched := 0

	for _, event := range events {
		// Skip already-seen events.
		if !firstPoll && event.ID <= lastEventID {
			continue
		}
		// On first poll, only process events from the last hour.
		if firstPoll && event.CreatedAt.Before(cutoff) {
			continue
		}
		highestID = event.ID

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
		if commits, ok := payload["commits"].([]interface{}); ok && len(commits) > 0 {
			if last, ok := commits[len(commits)-1].(map[string]interface{}); ok {
				if s, ok := last["sha"].(string); ok {
					sha = s
				}
			}
		}

		eventRepo := event.Repo.Name

		// Match against each schedule.
		for _, sched := range schedules {
			if !sched.Trigger.Matches(eventType, action, eventRepo, branch) {
				continue
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

			session, err := p.dispatcher.DispatchTask(ctx, taskReq, sched.Owner)
			if err != nil {
				log.Printf("poller: error dispatching schedule %s for %s: %v", sched.Name, eventRepo, err)
				continue
			}

			// Store event context on the session.
			meta := map[string]string{
				"GITHUB_EVENT":     eventType,
				"GITHUB_REPO":      eventRepo,
				"GITHUB_REF":       branch,
				"GITHUB_SHA":       sha,
				"GITHUB_PR_NUMBER": prNumber,
			}
			metaJSON, _ := json.Marshal(meta)
			_, _ = p.db.Exec(ctx,
				`UPDATE sessions SET prompt = prompt || E'\n\n[event: ' || $1 || ']' WHERE id = $2`,
				string(metaJSON), session.ID)

			dispatched++
			log.Printf("poller: dispatched %s for %s %s/%s (event %s)", sched.Name, eventType, eventRepo, action, event.ID)
		}
	}

	// Update poll state.
	if highestID == "" {
		highestID = lastEventID
	}
	_, _ = p.db.Exec(ctx,
		`INSERT INTO github_poll_state (repo, etag, last_event_id, last_polled_at) VALUES ($1, $2, $3, NOW())
		ON CONFLICT (repo) DO UPDATE SET etag = $2, last_event_id = $3, last_polled_at = NOW()`,
		repo, newEtag, highestID)

	if dispatched > 0 {
		log.Printf("poller: dispatched %d task(s) from %s", dispatched, repo)
	}
}
