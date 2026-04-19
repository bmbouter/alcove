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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CIGateMonitor watches PRs created by tasks with ci_gate config and
// retries on CI failure using the system LLM to analyze logs.
type CIGateMonitor struct {
	db         *pgxpool.Pool
	dispatcher *Dispatcher
	credStore  *CredentialStore
	llm        *BridgeLLM
	client     *http.Client
	mu         sync.Mutex
	watching   map[string]bool // session IDs being monitored
}

// NewCIGateMonitor creates a CIGateMonitor.
func NewCIGateMonitor(db *pgxpool.Pool, dispatcher *Dispatcher, credStore *CredentialStore, llm *BridgeLLM) *CIGateMonitor {
	return &CIGateMonitor{
		db:         db,
		dispatcher: dispatcher,
		credStore:  credStore,
		llm:        llm,
		client:     &http.Client{Timeout: 30 * time.Second},
		watching:   make(map[string]bool),
	}
}

// parsePRArtifact extracts the repo slug and PR number from a PR artifact.
// Skiff-init writes: Type="pull_request", URL="owner/repo", Ref="42"
func parsePRArtifact(a internal.Artifact) (repo string, prNumber int, ok bool) {
	if a.Type != "pull_request" && a.Type != "pr" {
		return "", 0, false
	}
	if a.URL == "" || a.Ref == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(a.Ref)
	if err != nil || n == 0 {
		return "", 0, false
	}
	return a.URL, n, true
}

// OnTaskCompleted is called when a task finishes. It checks for PR artifacts
// and ci_gate config, and starts CI monitoring if applicable.
func (m *CIGateMonitor) OnTaskCompleted(ctx context.Context, sessionID string, artifacts []internal.Artifact) {
	// Find PR artifact.
	var prRepo string
	var prNumber int
	for _, a := range artifacts {
		r, n, ok := parsePRArtifact(a)
		if ok {
			prRepo = r
			prNumber = n
			break
		}
	}
	if prNumber == 0 || prRepo == "" {
		return
	}

	// Look up agent definition to check for ci_gate config.
	var sourceKey string
	var owner string
	err := m.db.QueryRow(ctx,
		`SELECT COALESCE(s.source_key, ''), s.submitter FROM sessions sess
         JOIN schedules s ON s.source_key != ''
         WHERE sess.id = $1
         LIMIT 1`, sessionID).Scan(&sourceKey, &owner)
	if err != nil || sourceKey == "" {
		// Try getting owner from session directly.
		_ = m.db.QueryRow(ctx, `SELECT submitter FROM sessions WHERE id = $1`, sessionID).Scan(&owner)
	}

	// Look up ci_gate config from agent definition.
	var parsedJSON []byte
	if sourceKey != "" {
		_ = m.db.QueryRow(ctx,
			`SELECT parsed FROM agent_definitions WHERE source_key = $1`, sourceKey,
		).Scan(&parsedJSON)
	}

	// Also try matching by prompt similarity if source_key lookup fails.
	if parsedJSON == nil {
		_ = m.db.QueryRow(ctx,
			`SELECT td.parsed FROM sessions sess
             JOIN schedules sch ON sch.source_key != ''
             JOIN agent_definitions td ON td.source_key = sch.source_key
             WHERE sess.id = $1 LIMIT 1`, sessionID,
		).Scan(&parsedJSON)
	}

	if parsedJSON == nil {
		return
	}

	var td TaskDefinition
	if json.Unmarshal(parsedJSON, &td) != nil || td.CIGate == nil {
		return
	}

	maxRetries := td.CIGate.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	timeout := td.CIGate.Timeout
	if timeout <= 0 {
		timeout = 900
	}

	// Check if this is already a retry (has parent ci_gate_state).
	var existingRetryCount int
	var originalSessionID string
	err = m.db.QueryRow(ctx,
		`SELECT retry_count, original_session_id FROM ci_gate_state WHERE session_id = $1`, sessionID,
	).Scan(&existingRetryCount, &originalSessionID)

	retryCount := 0
	if err == nil {
		retryCount = existingRetryCount
	} else {
		originalSessionID = sessionID
	}

	// Insert ci_gate_state for this PR.
	_, err = m.db.Exec(ctx,
		`INSERT INTO ci_gate_state (session_id, pr_repo, pr_number, retry_count, max_retries, status, original_session_id, task_def_source_key, owner)
         VALUES ($1, $2, $3, $4, $5, 'monitoring', $6, $7, $8)
         ON CONFLICT (session_id) DO UPDATE SET status = 'monitoring', updated_at = NOW()`,
		sessionID, prRepo, prNumber, retryCount, maxRetries, originalSessionID, sourceKey, owner)
	if err != nil {
		log.Printf("cigate: error inserting state for session %s: %v", sessionID, err)
		return
	}

	// Start monitoring in background.
	m.mu.Lock()
	if m.watching[sessionID] {
		m.mu.Unlock()
		return
	}
	m.watching[sessionID] = true
	m.mu.Unlock()

	go m.monitorPR(context.Background(), sessionID, prRepo, prNumber, timeout, owner)
}

// ciCheckRun represents a GitHub check run from the API response.
type ciCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

// monitorPR polls CI status on a PR until it passes, fails, or times out.
func (m *CIGateMonitor) monitorPR(ctx context.Context, sessionID, repo string, prNumber, timeoutSecs int, owner string) {
	defer func() {
		m.mu.Lock()
		delete(m.watching, sessionID)
		m.mu.Unlock()
	}()

	log.Printf("cigate: monitoring CI for PR %s#%d (session %s)", repo, prNumber, sessionID)

	token, _, err := m.credStore.AcquireSCMTokenForOwner(ctx, "github", owner)
	if err != nil {
		log.Printf("cigate: no GitHub credential for %s: %v", owner, err)
		m.updateStatus(ctx, sessionID, "error")
		return
	}

	deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
	pollInterval := 30 * time.Second

	for time.Now().Before(deadline) {
		// Get PR head SHA.
		prData, err := m.githubGet(ctx, token, fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber))
		if err != nil {
			log.Printf("cigate: error fetching PR %s#%d: %v", repo, prNumber, err)
			time.Sleep(pollInterval)
			continue
		}

		var pr struct {
			Head struct{ SHA string `json:"sha"` } `json:"head"`
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		}
		json.Unmarshal(prData, &pr)

		if pr.State != "open" || pr.Merged {
			log.Printf("cigate: PR %s#%d is %s (merged=%v), stopping monitor", repo, prNumber, pr.State, pr.Merged)
			m.updateStatus(ctx, sessionID, "passed")
			return
		}

		// Check CI status.
		checksData, err := m.githubGet(ctx, token, fmt.Sprintf("https://api.github.com/repos/%s/commits/%s/check-runs", repo, pr.Head.SHA))
		if err != nil {
			log.Printf("cigate: error fetching checks for %s#%d: %v", repo, prNumber, err)
			time.Sleep(pollInterval)
			continue
		}

		var checks struct {
			CheckRuns []ciCheckRun `json:"check_runs"`
		}
		json.Unmarshal(checksData, &checks)

		if len(checks.CheckRuns) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		allComplete := true
		anyFailed := false
		var failedChecks []string
		for _, cr := range checks.CheckRuns {
			if cr.Status != "completed" {
				allComplete = false
				break
			}
			if cr.Conclusion != "success" && cr.Conclusion != "skipped" {
				anyFailed = true
				failedChecks = append(failedChecks, fmt.Sprintf("%s (conclusion: %s, id: %d)", cr.Name, cr.Conclusion, cr.ID))
			}
		}

		if !allComplete {
			time.Sleep(pollInterval)
			continue
		}

		if !anyFailed {
			log.Printf("cigate: CI passed for PR %s#%d", repo, prNumber)
			m.updateStatus(ctx, sessionID, "passed")
			return
		}

		// CI failed — trigger retry.
		log.Printf("cigate: CI failed for PR %s#%d: %v", repo, prNumber, failedChecks)
		m.handleCIFailure(ctx, sessionID, repo, prNumber, token, checks.CheckRuns, owner)
		return
	}

	log.Printf("cigate: CI monitoring timed out for PR %s#%d", repo, prNumber)
	m.updateStatus(ctx, sessionID, "timeout")
}

// handleCIFailure fetches failure logs, uses system LLM to analyze them,
// and dispatches a retry task.
func (m *CIGateMonitor) handleCIFailure(ctx context.Context, sessionID, repo string, prNumber int, token string, checkRuns []ciCheckRun, owner string) {
	// Load ci_gate_state.
	var retryCount, maxRetries int
	var originalSessionID, sourceKey string
	err := m.db.QueryRow(ctx,
		`SELECT retry_count, max_retries, original_session_id, COALESCE(task_def_source_key, '') FROM ci_gate_state WHERE session_id = $1`,
		sessionID,
	).Scan(&retryCount, &maxRetries, &originalSessionID, &sourceKey)
	if err != nil {
		log.Printf("cigate: error loading state for %s: %v", sessionID, err)
		return
	}

	if retryCount >= maxRetries {
		log.Printf("cigate: max retries (%d) exhausted for PR %s#%d", maxRetries, repo, prNumber)
		m.updateStatus(ctx, sessionID, "exhausted")
		m.postExhaustedComment(ctx, repo, prNumber, token, retryCount)
		m.addLabel(ctx, repo, prNumber, token, "needs-human-review")
		return
	}

	// Fetch failure logs for failed checks.
	var failureSummary strings.Builder
	for _, cr := range checkRuns {
		if cr.Conclusion != "success" && cr.Conclusion != "skipped" && cr.Status == "completed" {
			// Fetch job log.
			logData, err := m.githubGet(ctx, token, fmt.Sprintf("https://api.github.com/repos/%s/actions/jobs/%d/logs", repo, cr.ID))
			if err != nil {
				failureSummary.WriteString(fmt.Sprintf("\n### %s (conclusion: %s)\nCould not fetch logs: %v\n", cr.Name, cr.Conclusion, err))
				continue
			}
			// Truncate logs to last 3000 chars to keep within LLM context.
			logStr := string(logData)
			if len(logStr) > 3000 {
				logStr = logStr[len(logStr)-3000:]
			}
			failureSummary.WriteString(fmt.Sprintf("\n### %s (conclusion: %s)\n```\n%s\n```\n", cr.Name, cr.Conclusion, logStr))
		}
	}

	// Get the PR branch name.
	prData, _ := m.githubGet(ctx, token, fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repo, prNumber))
	var prInfo struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	json.Unmarshal(prData, &prInfo)

	// Look up original agent definition for full prompt, profiles, repo, provider, timeout.
	var taskReq TaskRequest
	var originalPrompt string
	if sourceKey != "" {
		var parsedJSON []byte
		_ = m.db.QueryRow(ctx,
			`SELECT parsed FROM agent_definitions WHERE source_key = $1`, sourceKey,
		).Scan(&parsedJSON)
		if parsedJSON != nil {
			var td TaskDefinition
			if json.Unmarshal(parsedJSON, &td) == nil {
				taskReq = td.ToTaskRequest()
				originalPrompt = td.Prompt
			}
		}
	}

	// Fallback: recover key fields from the original session if task def lookup fails.
	if len(taskReq.Repos) == 0 || originalPrompt == "" {
		var origPrompt, origProvider string
		_ = m.db.QueryRow(ctx,
			`SELECT COALESCE(prompt, ''), COALESCE(provider, '') FROM sessions WHERE id = $1`,
			originalSessionID,
		).Scan(&origPrompt, &origProvider)
		if taskReq.Provider == "" {
			taskReq.Provider = origProvider
		}
		if originalPrompt == "" {
			originalPrompt = origPrompt
		}
	}

	// Compose the retry prompt: CI failure context + modified original prompt.
	ciContext := m.composeCIFailureContext(ctx, repo, prNumber, prInfo.Head.Ref, prInfo.Title, failureSummary.String(), retryCount+1, maxRetries)
	taskReq.Prompt = ciContext + "\n\n" + modifyPromptForRetry(originalPrompt, prInfo.Head.Ref, repo, prNumber)

	// Set task metadata for session storage.
	taskReq.TaskName = "CI Retry"
	taskReq.TriggerType = "event"
	taskReq.TriggerRef = fmt.Sprintf("%s#%d", repo, prNumber)

	// Resolve team_id from the original session.
	var ciTeamID string
	_ = m.db.QueryRow(ctx, `SELECT team_id FROM sessions WHERE id = $1`, sessionID).Scan(&ciTeamID)

	// Dispatch retry task.
	newSession, err := m.dispatcher.DispatchTask(ctx, taskReq, owner, ciTeamID)
	if err != nil {
		log.Printf("cigate: error dispatching retry for PR %s#%d: %v", repo, prNumber, err)
		m.updateStatus(ctx, sessionID, "error")
		return
	}

	log.Printf("cigate: dispatched retry %d/%d for PR %s#%d (new session %s)", retryCount+1, maxRetries, repo, prNumber, newSession.ID)

	// Update current session status.
	m.updateStatus(ctx, sessionID, "retrying")

	// Create ci_gate_state for the new retry session.
	_, _ = m.db.Exec(ctx,
		`INSERT INTO ci_gate_state (session_id, pr_repo, pr_number, retry_count, max_retries, status, original_session_id, task_def_source_key, owner)
         VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8)`,
		newSession.ID, repo, prNumber, retryCount+1, maxRetries, originalSessionID, sourceKey, owner)
}

// composeCIFailureContext generates the CI failure preamble (not a full prompt).
// It uses the system LLM to analyze failure logs if available, otherwise includes raw logs.
func (m *CIGateMonitor) composeCIFailureContext(ctx context.Context, repo string, prNumber int, branch, title, failureLogs string, attempt, maxAttempts int) string {
	var analysis string
	if m.llm != nil && m.llm.Available() {
		systemPrompt := `You are a CI failure analysis assistant. Given CI failure logs, compose a brief analysis of what failed and why. Be specific about files and errors. Output only the analysis, no fencing.`
		userPrompt := fmt.Sprintf("PR: %s#%d\nBranch: %s\nAttempt: %d of %d\n\nCI Logs:\n%s", repo, prNumber, branch, attempt, maxAttempts, failureLogs)
		result, err := m.llm.Complete(ctx, systemPrompt, userPrompt, 1500)
		if err == nil && result != "" {
			analysis = result
		} else {
			log.Printf("cigate: LLM analysis failed: %v", err)
			analysis = failureLogs
		}
	} else {
		analysis = failureLogs
	}

	return fmt.Sprintf(`## CI Retry Context

**IMPORTANT**: You are fixing CI failures on an existing PR, NOT implementing from scratch.

- Repository: %s
- PR: #%d (branch: %s)
- Title: %s
- CI fix attempt: %d of %d

### What You Must Do
1. The repo is already cloned. Fetch and checkout the PR branch:
   git fetch origin %s && git checkout %s
2. Fix the CI failures described below
3. Run local validation: go build ./... && go vet ./...
4. Commit and push: git add -A && git commit -m "Fix CI failures (attempt %d)" && git push origin %s
5. Write the PR artifact: echo '{"repo": "%s", "number": %d}' > /tmp/alcove-pr.json
6. Do NOT create a new PR or new branch

### CI Failure Analysis
%s

---
`, repo, prNumber, branch, title, attempt, maxAttempts,
		branch, branch, attempt, branch, repo, prNumber, analysis)
}

// modifyPromptForRetry prepends a CI-retry override to the original task prompt.
func modifyPromptForRetry(originalPrompt, branch, repo string, prNumber int) string {
	override := fmt.Sprintf(`## OVERRIDE: CI Retry Mode

This task is running in CI retry mode. An existing PR needs CI fixes.
- Do NOT create a new branch or new PR
- Work on branch: %s
- Fix ONLY the CI failures in the CI Retry Context above
- Push to the existing branch when done
- Write PR artifact: echo '{"repo": "%s", "number": %d}' > /tmp/alcove-pr.json

The original task instructions follow below for project context and conventions.
Ignore any instructions about creating branches or PRs — use the existing one.

---

`, branch, repo, prNumber)
	return override + originalPrompt
}

// githubGet performs an authenticated GET request to the GitHub API.
func (m *CIGateMonitor) githubGet(ctx context.Context, token, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "alcove-cigate")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return body, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}
	return body, nil
}

func (m *CIGateMonitor) updateStatus(ctx context.Context, sessionID, status string) {
	_, _ = m.db.Exec(ctx,
		`UPDATE ci_gate_state SET status = $1, updated_at = NOW() WHERE session_id = $2`,
		status, sessionID)
}

func (m *CIGateMonitor) postExhaustedComment(ctx context.Context, repo string, prNumber int, token string, retryCount int) {
	comment := fmt.Sprintf(`{"body":"**CI Gate: Retries Exhausted**\n\nThis PR has failed CI %d times with automated fix attempts. The CI failures could not be resolved automatically.\n\nPlease review the CI logs and fix manually."}`, retryCount)

	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/comments", repo, prNumber),
		strings.NewReader(comment))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "alcove-cigate")
	m.client.Do(req)
}

func (m *CIGateMonitor) addLabel(ctx context.Context, repo string, prNumber int, token, label string) {
	body := fmt.Sprintf(`{"labels":["%s"]}`, label)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("https://api.github.com/repos/%s/issues/%d/labels", repo, prNumber),
		strings.NewReader(body))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "alcove-cigate")
	m.client.Do(req)
}

// RecoverMonitors resumes CI monitoring for sessions that were being
// watched when Bridge last shut down. Called once on startup.
func (m *CIGateMonitor) RecoverMonitors(ctx context.Context) {
	rows, err := m.db.Query(ctx,
		`SELECT session_id, pr_repo, pr_number, max_retries, owner
		 FROM ci_gate_state WHERE status = 'monitoring'`)
	if err != nil {
		log.Printf("cigate: error recovering monitors: %v", err)
		return
	}
	defer rows.Close()

	recovered := 0
	for rows.Next() {
		var sessionID, prRepo, owner string
		var prNumber, maxRetries int
		if err := rows.Scan(&sessionID, &prRepo, &prNumber, &maxRetries, &owner); err != nil {
			continue
		}

		// Default timeout for recovered monitors (15 minutes).
		timeout := 900

		m.mu.Lock()
		if m.watching[sessionID] {
			m.mu.Unlock()
			continue
		}
		m.watching[sessionID] = true
		m.mu.Unlock()

		go m.monitorPR(context.Background(), sessionID, prRepo, prNumber, timeout, owner)
		recovered++
	}

	if recovered > 0 {
		log.Printf("cigate: recovered %d PR monitor(s) from previous session", recovered)
	}
}

// truncateStr shortens a string for log messages.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
