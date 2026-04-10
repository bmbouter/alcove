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

	// Look up task definition to check for ci_gate config.
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

	// Look up ci_gate config from task definition.
	var parsedJSON []byte
	if sourceKey != "" {
		_ = m.db.QueryRow(ctx,
			`SELECT parsed FROM task_definitions WHERE source_key = $1`, sourceKey,
		).Scan(&parsedJSON)
	}

	// Also try matching by prompt similarity if source_key lookup fails.
	if parsedJSON == nil {
		_ = m.db.QueryRow(ctx,
			`SELECT td.parsed FROM sessions sess
             JOIN schedules sch ON sch.source_key != ''
             JOIN task_definitions td ON td.source_key = sch.source_key
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

	// Compose retry prompt using system LLM if available, otherwise use template.
	retryPrompt := m.composeRetryPrompt(ctx, repo, prNumber, prInfo.Head.Ref, prInfo.Title, failureSummary.String(), retryCount+1, maxRetries)

	// Look up original task definition for profiles, repo, provider, timeout.
	var taskReq TaskRequest
	if sourceKey != "" {
		var parsedJSON []byte
		_ = m.db.QueryRow(ctx,
			`SELECT parsed FROM task_definitions WHERE source_key = $1`, sourceKey,
		).Scan(&parsedJSON)
		if parsedJSON != nil {
			var td TaskDefinition
			if json.Unmarshal(parsedJSON, &td) == nil {
				taskReq = td.ToTaskRequest()
			}
		}
	}

	// Override prompt with retry-specific one.
	taskReq.Prompt = retryPrompt

	// Dispatch retry task.
	newSession, err := m.dispatcher.DispatchTask(ctx, taskReq, owner)
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

// composeRetryPrompt uses the system LLM to create a targeted retry prompt,
// or falls back to a template if no LLM is available.
func (m *CIGateMonitor) composeRetryPrompt(ctx context.Context, repo string, prNumber int, branch, title, failureLogs string, attempt, maxAttempts int) string {
	if m.llm != nil && m.llm.Available() {
		systemPrompt := `You are a CI failure analysis assistant. Given CI failure logs from a GitHub pull request, compose a concise, actionable prompt for an AI coding agent that will fix the failures. The agent has the repo cloned and can read/edit files and push to the branch. Focus on:
1. What specifically failed (compilation errors, test failures, lint issues)
2. The likely root cause
3. Specific files/lines to investigate
4. A clear action plan

Output ONLY the prompt text, no explanations or markdown fencing.`

		userPrompt := fmt.Sprintf(`PR: %s#%d
Branch: %s
Title: %s
Attempt: %d of %d

CI Failure Logs:
%s`, repo, prNumber, branch, title, attempt, maxAttempts, failureLogs)

		analysis, err := m.llm.Complete(ctx, systemPrompt, userPrompt, 2000)
		if err == nil && analysis != "" {
			return fmt.Sprintf(`You are fixing CI failures on an existing pull request.

## Context
- Repository: %s
- PR: #%d (branch: %s)
- Title: %s
- This is CI fix attempt %d of %d

## Environment
Use curl with $GITHUB_API_URL and $GITHUB_TOKEN for GitHub API calls.
Write JSON payloads to files before POSTing.

## Instructions
1. Checkout the existing branch: git fetch origin %s && git checkout %s
2. Read the CI failure analysis below and fix the issues
3. Run any available local validation before pushing (go build, go test, go vet, etc.)
4. Commit and push to the same branch
5. Do NOT create a new PR — push to the existing branch

## CI Failure Analysis
%s

## Important
- Fix ONLY the CI failures — do not refactor or add features
- Run local validation before pushing
- If you cannot fix a failure, leave a comment on PR #%d explaining what you tried
`, repo, prNumber, branch, title, attempt, maxAttempts, branch, branch, analysis, prNumber)
		}
		log.Printf("cigate: LLM analysis failed, using template: %v", err)
	}

	// Fallback template.
	return fmt.Sprintf(`You are fixing CI failures on an existing pull request.

## Context
- Repository: %s
- PR: #%d (branch: %s)
- Title: %s
- This is CI fix attempt %d of %d

## Environment
Use curl with $GITHUB_API_URL and $GITHUB_TOKEN for GitHub API calls.
Write JSON payloads to files before POSTing.

## Instructions
1. Checkout the existing branch: git fetch origin %s && git checkout %s
2. Fetch the CI check runs to understand what failed:
   curl -s -H "Authorization: token $GITHUB_TOKEN" "$GITHUB_API_URL/repos/%s/commits/%s/check-runs"
3. For each failed check, fetch the log and analyze the error
4. Fix the issues in the code
5. Run any available local validation (go build, go test, go vet, etc.)
6. Commit and push to the same branch
7. Do NOT create a new PR — push to the existing branch

## CI Failure Summary
%s

## Important
- Fix ONLY the CI failures — do not refactor or add features
- Run local validation before pushing
- If you cannot fix a failure, leave a comment on PR #%d explaining what you tried
`, repo, prNumber, branch, title, attempt, maxAttempts, branch, branch, repo, branch, failureLogs, prNumber)
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

// truncateStr shortens a string for log messages.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
