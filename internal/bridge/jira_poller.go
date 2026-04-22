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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// JiraPoller polls JIRA for recently updated issues and triggers workflows
// whose definitions include a jira trigger that matches.
type JiraPoller struct {
	db             *pgxpool.Pool
	credStore      *CredentialStore
	workflowEngine *WorkflowEngine
	defStore       *AgentDefStore
	baseURL        string // e.g., "https://redhat.atlassian.net"
	pollInterval   time.Duration
	lastPollTime   time.Time
}

// NewJiraPoller creates a JiraPoller with the given dependencies.
func NewJiraPoller(db *pgxpool.Pool, credStore *CredentialStore, we *WorkflowEngine, defStore *AgentDefStore) *JiraPoller {
	return &JiraPoller{
		db:             db,
		credStore:      credStore,
		workflowEngine: we,
		defStore:       defStore,
		baseURL:        "https://redhat.atlassian.net",
		pollInterval:   2 * time.Minute,
		lastPollTime:   time.Now().Add(-5 * time.Minute),
	}
}

// Start begins the JIRA polling loop in the current goroutine. It blocks until
// the context is cancelled.
func (jp *JiraPoller) Start(ctx context.Context) {
	ticker := time.NewTicker(jp.pollInterval)
	defer ticker.Stop()

	// Initial poll after 30 seconds
	time.Sleep(30 * time.Second)
	jp.PollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jp.PollAll(ctx)
		}
	}
}

// jiraPollTarget holds the information needed to match and dispatch a JIRA-
// triggered workflow.
type jiraPollTarget struct {
	workflowID string
	teamID     string
	trigger    *JiraTrigger
}

// PollAll queries all workflows with JIRA triggers and polls JIRA for matching
// recently-updated issues.
func (jp *JiraPoller) PollAll(ctx context.Context) {
	rows, err := jp.db.Query(ctx, `
		SELECT w.id, w.name, w.parsed, w.team_id
		FROM workflows w
		WHERE w.parsed IS NOT NULL
	`)
	if err != nil {
		log.Printf("jira-poller: error querying workflows: %v", err)
		return
	}
	defer rows.Close()

	var targets []jiraPollTarget

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

		if wd.Trigger != nil && wd.Trigger.Jira != nil {
			targets = append(targets, jiraPollTarget{
				workflowID: wfID,
				teamID:     teamID,
				trigger:    wd.Trigger.Jira,
			})
		}
	}

	if len(targets) == 0 {
		return
	}

	// Group by team to minimize credential lookups.
	teamTargets := make(map[string][]jiraPollTarget)
	for _, t := range targets {
		teamTargets[t.teamID] = append(teamTargets[t.teamID], t)
	}

	for teamID, tgts := range teamTargets {
		jp.pollForTeam(ctx, teamID, tgts)
	}

	jp.lastPollTime = time.Now()
}

func (jp *JiraPoller) pollForTeam(ctx context.Context, teamID string, targets []jiraPollTarget) {
	token, _, err := jp.credStore.AcquireSCMTokenForOwner(ctx, "jira", teamID)
	if err != nil {
		log.Printf("jira-poller: no jira credential for team %s: %v", teamID, err)
		return
	}

	// Collect all projects from targets.
	projectSet := make(map[string]bool)
	for _, t := range targets {
		for _, p := range t.trigger.Projects {
			projectSet[strings.ToUpper(p)] = true
		}
	}

	// Build JQL for recently updated issues.
	var projects []string
	for p := range projectSet {
		projects = append(projects, p)
	}

	minutesSinceLastPoll := int(time.Since(jp.lastPollTime).Minutes()) + 1
	jql := fmt.Sprintf("project IN (%s) AND updated >= \"-%dm\" ORDER BY updated DESC",
		strings.Join(projects, ","), minutesSinceLastPoll)

	// Search JIRA.
	searchURL := fmt.Sprintf("%s/rest/api/2/search?jql=%s&maxResults=50&fields=key,summary,status,labels,components,description,issuetype",
		jp.baseURL, url.QueryEscape(jql))

	data, err := jp.jiraRequest(ctx, token, "GET", searchURL, nil)
	if err != nil {
		log.Printf("jira-poller: search error: %v", err)
		return
	}

	var searchResult struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary     string `json:"summary"`
				Description string `json:"description"`
				Status      struct {
					Name string `json:"name"`
				} `json:"status"`
				Labels     []string `json:"labels"`
				Components []struct {
					Name string `json:"name"`
				} `json:"components"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(data, &searchResult); err != nil {
		log.Printf("jira-poller: error parsing search results: %v", err)
		return
	}

	log.Printf("jira-poller: found %d recently updated issues in %v", len(searchResult.Issues), projects)

	// Check each issue against each target's trigger.
	for _, issue := range searchResult.Issues {
		issueProject := strings.Split(issue.Key, "-")[0]
		var issueComponents []string
		for _, c := range issue.Fields.Components {
			issueComponents = append(issueComponents, c.Name)
		}

		for _, target := range targets {
			if target.trigger.Matches(issueProject, issueComponents, issue.Fields.Labels) {
				// Check dedup — don't dispatch the same issue twice for the same workflow.
				var count int
				jp.db.QueryRow(ctx, `
					SELECT COUNT(*) FROM workflow_runs
					WHERE workflow_id = $1 AND trigger_ref = $2
					AND created_at > NOW() - INTERVAL '24 hours'
				`, target.workflowID, issue.Key).Scan(&count)

				if count > 0 {
					continue // Already dispatched recently
				}

				log.Printf("jira-poller: triggering workflow %s for issue %s", target.workflowID, issue.Key)

				triggerContext := map[string]interface{}{
					"issue_key":    issue.Key,
					"issue_title":  issue.Fields.Summary,
					"issue_body":   issue.Fields.Description,
					"issue_url":    fmt.Sprintf("%s/browse/%s", jp.baseURL, issue.Key),
					"issue_status": issue.Fields.Status.Name,
					"issue_labels": issue.Fields.Labels,
					"issue_type":   issue.Fields.IssueType.Name,
				}

				_, err := jp.workflowEngine.StartWorkflowRun(ctx, target.workflowID, "jira", issue.Key, target.teamID, triggerContext)
				if err != nil {
					log.Printf("jira-poller: error starting workflow for %s: %v", issue.Key, err)
				}
			}
		}
	}
}

func (jp *JiraPoller) jiraRequest(ctx context.Context, credential, method, reqURL string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = strings.NewReader(string(body))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, reqBody)
	if err != nil {
		return nil, err
	}

	// JIRA Cloud uses Basic auth: email:api_token
	// The credential is stored as the raw API token; we need the email prefix.
	// Convention: credential stored as "email:token" or just "token".
	if strings.Contains(credential, ":") {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential)))
	} else {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "alcove-jira-poller")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
