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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/bmbouter/alcove/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

//go:embed templates/*.yml
var templateFS embed.FS

// API holds the HTTP handlers for the Bridge REST API.
type API struct {
	dispatcher       *Dispatcher
	db               *pgxpool.Pool
	cfg              *Config
	scheduler        *Scheduler
	credStore        *CredentialStore
	toolStore        *ToolStore
	profileStore     *ProfileStore
	settingsStore    *SettingsStore
	llm              *BridgeLLM
	defStore         *AgentDefStore
	syncer           *AgentRepoSyncer
	authStore        auth.Authenticator // for TBR associations (rh-identity backend)
	workflowEngine   *WorkflowEngine
	teamStore        *TeamStore
	catalogItemStore *CatalogItemStore
}

// NewAPI creates the API handler set.
func NewAPI(dispatcher *Dispatcher, db *pgxpool.Pool, cfg *Config, scheduler *Scheduler, credStore *CredentialStore, toolStore *ToolStore, profileStore *ProfileStore, settingsStore *SettingsStore, llm *BridgeLLM, defStore *AgentDefStore, syncer *AgentRepoSyncer, authStore auth.Authenticator, workflowEngine *WorkflowEngine, teamStore *TeamStore) *API {
	return &API{
		dispatcher:       dispatcher,
		db:               db,
		cfg:              cfg,
		scheduler:        scheduler,
		credStore:        credStore,
		toolStore:        toolStore,
		profileStore:     profileStore,
		settingsStore:    settingsStore,
		llm:              llm,
		defStore:         defStore,
		syncer:           syncer,
		authStore:        authStore,
		workflowEngine:   workflowEngine,
		teamStore:        teamStore,
		catalogItemStore: NewCatalogItemStore(db),
	}
}

// RegisterRoutes registers all API routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/health", a.handleHealth)
	mux.HandleFunc("/api/v1/sessions", a.handleSessions)
	mux.HandleFunc("/api/v1/sessions/", a.handleSessionByID)
	mux.HandleFunc("/api/v1/providers", a.handleProviders)
	mux.HandleFunc("/api/v1/schedules", a.handleSchedules)
	mux.HandleFunc("/api/v1/schedules/", a.handleScheduleByID)
	mux.HandleFunc("/api/v1/credentials", a.handleCredentials)
	mux.HandleFunc("/api/v1/credentials/", a.handleCredentialByID)
	mux.HandleFunc("/api/v1/tools", a.handleTools)
	mux.HandleFunc("/api/v1/tools/", a.handleToolByID)
	mux.HandleFunc("/api/v1/security-profiles", a.handleSecurityProfiles)
	mux.HandleFunc("/api/v1/security-profiles/", a.handleSecurityProfileByID)
	mux.HandleFunc("/api/v1/internal/token-refresh", a.handleTokenRefresh)
	mux.HandleFunc("/api/v1/admin/settings/llm", a.handleAdminSettingsLLM)
mux.HandleFunc("/api/v1/user/settings/agent-repos", a.handleUserSettingsAgentRepos)
	mux.HandleFunc("/api/v1/agent-repos/validate", a.handleAgentRepoValidate)
	mux.HandleFunc("/api/v1/agent-definitions", a.handleAgentDefinitions)
	mux.HandleFunc("/api/v1/agent-definitions/sync", a.handleAgentDefinitionsSync)
	mux.HandleFunc("/api/v1/agent-definitions/", a.handleAgentDefinitionByID)
	mux.HandleFunc("/api/v1/agent-templates", a.handleAgentTemplates)
	mux.HandleFunc("/api/v1/webhooks/github", a.handleWebhookGitHub)
	mux.HandleFunc("/api/v1/admin/settings/webhook", a.handleAdminSettingsWebhook)
	mux.HandleFunc("/api/v1/system-info", a.handleSystemInfo)
	mux.HandleFunc("/api/v1/admin/system-state", a.handleSystemState)
	mux.HandleFunc("/api/v1/auth/tbr-associations", a.handleTBRAssociations)
	mux.HandleFunc("/api/v1/auth/tbr-associations/", a.handleTBRAssociationByID)
	mux.HandleFunc("/api/v1/auth/api-tokens", a.handlePersonalAPITokens)
	mux.HandleFunc("/api/v1/auth/api-tokens/", a.handlePersonalAPITokenByID)
	mux.HandleFunc("/api/v1/workflows", a.handleWorkflows)
	mux.HandleFunc("/api/v1/workflow-runs", a.handleWorkflowRuns)
	mux.HandleFunc("/api/v1/workflow-runs/", a.handleWorkflowRunByID)
	mux.HandleFunc("/api/v1/bridge-actions", a.handleBridgeActions)
	mux.HandleFunc("/api/v1/catalog", a.handleCatalog)
	mux.HandleFunc("/api/v1/teams", a.handleTeams)
	mux.HandleFunc("/api/v1/teams/", a.handleTeam)
}

// --- Health ---

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Check database connectivity.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbOK := true
	if err := a.db.Ping(ctx); err != nil {
		dbOK = false
	}

	status := "healthy"
	code := http.StatusOK
	if !dbOK {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}

	respondJSON(w, code, map[string]any{
		"status":  status,
		"runtime": a.cfg.RuntimeType,
		"db":      dbOK,
		"version": a.cfg.Version,
	})
}

// --- System Info ---

func (a *API) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	eff := ResolveEffectiveLLM(a.cfg)
	llmStatus := map[string]interface{}{
		"configured": eff.Configured,
	}
	if eff.Configured {
		llmStatus["provider"] = eff.Provider
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"version":      a.cfg.Version,
		"runtime":      a.cfg.RuntimeType,
		"auth_backend": a.cfg.AuthBackend,
		"system_llm":   llmStatus,
	})
}

// --- Sessions ---

func (a *API) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		// Check system mode before dispatching.
		if mode, _ := a.settingsStore.GetSystemMode(r.Context()); mode == "paused" {
			respondError(w, http.StatusServiceUnavailable, "system is paused for maintenance — new sessions are not being accepted")
			return
		}

		var req TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if req.Prompt == "" {
			respondError(w, http.StatusBadRequest, "prompt is required")
			return
		}

		submitter := r.Header.Get("X-Alcove-User")
		if submitter == "" {
			submitter = "anonymous"
		}

		req.TriggerType = "manual"

		teamID := getActiveTeamID(r)
		session, err := a.dispatcher.DispatchTask(r.Context(), req, submitter, teamID)
		if err != nil {
			log.Printf("error: dispatch failed: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to dispatch session: "+err.Error())
			return
		}

		respondJSON(w, http.StatusCreated, session)
	case http.MethodGet:
		query := r.URL.Query()
		status := query.Get("status")
		repo := query.Get("repo")
		since := query.Get("since")
		until := query.Get("until")
		teamID := getActiveTeamID(r)

		pageStr := query.Get("page")
		perPageStr := query.Get("per_page")

		page := 1
		perPage := 50
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
		if pp, err := strconv.Atoi(perPageStr); err == nil && pp > 0 && pp <= 100 {
			perPage = pp
		}

		sessions, total, err := a.listSessions(r.Context(), status, repo, since, until, teamID, page, perPage)
		if err != nil {
			log.Printf("error: listing sessions: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list sessions")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"sessions": sessions,
			"count":    len(sessions),
			"total":    total,
			"page":     page,
			"per_page": perPage,
			"pages":    (total + perPage - 1) / perPage,
		})
	case http.MethodDelete:
		// Bulk delete sessions
		a.handleBulkDeleteSessions(w, r)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/sessions/{id} or /api/v1/sessions/{id}/transcript
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	parts := strings.SplitN(path, "/", 2)
	sessionID := parts[0]

	if sessionID == "" {
		respondError(w, http.StatusBadRequest, "session id required")
		return
	}

	if len(parts) == 2 {
		switch parts[1] {
		case "transcript":
			if r.Method == http.MethodPost {
				a.handleAppendTranscript(w, r, sessionID)
			} else {
				a.handleTranscript(w, r, sessionID)
			}
			return
		case "status":
			if r.Method == http.MethodPost {
				a.handleUpdateStatus(w, r, sessionID)
			} else {
				respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		case "proxy-log":
			switch r.Method {
			case http.MethodPost:
				a.handleAppendProxyLog(w, r, sessionID)
			case http.MethodGet:
				a.handleGetProxyLog(w, r, sessionID)
			default:
				respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		a.handleGetSession(w, r, sessionID)
	case http.MethodDelete:
		// Check if this is a delete request (vs cancel)
		if r.URL.Query().Get("action") == "delete" {
			a.handleDeleteSession(w, r, sessionID)
		} else {
			a.handleCancelSession(w, r, sessionID)
		}
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := a.checkTeamAccess(r.Context(), sessionID, r); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	session, err := a.getSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	// Also fetch transcript, proxy log, and runtime config.
	type sessionDetail struct {
		internal.Session
		Transcript    json.RawMessage `json:"transcript,omitempty"`
		ProxyLog      json.RawMessage `json:"proxy_log,omitempty"`
		RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
	}

	detail := sessionDetail{Session: *session}

	var transcript, proxyLog, runtimeConfig []byte
	_ = a.db.QueryRow(r.Context(),
		`SELECT transcript, proxy_log, runtime_config FROM sessions WHERE id = $1`, sessionID,
	).Scan(&transcript, &proxyLog, &runtimeConfig)

	if transcript != nil {
		detail.Transcript = transcript
	}
	if proxyLog != nil {
		detail.ProxyLog = proxyLog
	}
	if runtimeConfig != nil {
		detail.RuntimeConfig = runtimeConfig
	}

	respondJSON(w, http.StatusOK, detail)
}

func (a *API) handleCancelSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := a.checkTeamAccess(r.Context(), sessionID, r); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	if err := a.dispatcher.CancelSession(r.Context(), sessionID); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "cancelled",
		"session": sessionID,
	})
}

func (a *API) handleDeleteSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := a.checkTeamAccess(r.Context(), sessionID, r); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	// Get the current session to check its status
	session, err := a.getSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	// Only allow deleting sessions in terminal states
	terminalStates := map[string]bool{
		"completed": true,
		"error":     true,
		"timeout":   true,
		"cancelled": true,
	}

	if !terminalStates[session.Status] {
		respondError(w, http.StatusBadRequest, "can only delete sessions in terminal states (completed, error, timeout, cancelled). Running sessions must be cancelled first.")
		return
	}

	// Delete the session record, transcript, and proxy log
	result, err := a.db.Exec(r.Context(), `DELETE FROM sessions WHERE id = $1`, sessionID)
	if err != nil {
		log.Printf("error: deleting session %s: %v", sessionID, err)
		respondError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}

	if result.RowsAffected() == 0 {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":  "deleted",
		"session": sessionID,
	})
}

func (a *API) handleBulkDeleteSessions(w http.ResponseWriter, r *http.Request) {
	teamID := getActiveTeamID(r)
	if teamID == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	var req struct {
		Status string   `json:"status,omitempty"`
		Before string   `json:"before,omitempty"` // RFC3339 datetime or duration like "7d", "30d"
		IDs    []string `json:"ids,omitempty"`    // Specific session IDs to delete
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Build WHERE clause for deletion criteria
	whereClause := " WHERE team_id = $1"
	args := []any{teamID}
	argN := 2

	// Only allow deleting terminal state sessions
	terminalStates := []string{"completed", "error", "timeout", "cancelled"}
	whereClause += fmt.Sprintf(" AND outcome = ANY($%d)", argN)
	args = append(args, terminalStates)
	argN++

	if req.Status != "" {
		// Validate status is a terminal state
		validStatus := false
		for _, state := range terminalStates {
			if req.Status == state {
				validStatus = true
				break
			}
		}
		if !validStatus {
			respondError(w, http.StatusBadRequest, "status must be one of: completed, error, timeout, cancelled")
			return
		}
		whereClause += fmt.Sprintf(" AND outcome = $%d", argN)
		args = append(args, req.Status)
		argN++
	}

	if req.Before != "" {
		// Parse "before" parameter - can be RFC3339 datetime or duration
		var beforeTime time.Time
		var err error

		if strings.HasSuffix(req.Before, "d") {
			// Duration format like "7d", "30d"
			daysStr := strings.TrimSuffix(req.Before, "d")
			days, parseErr := strconv.Atoi(daysStr)
			if parseErr != nil {
				respondError(w, http.StatusBadRequest, "invalid before parameter: must be RFC3339 datetime or duration like '7d'")
				return
			}
			beforeTime = time.Now().UTC().AddDate(0, 0, -days)
		} else {
			// RFC3339 datetime format
			beforeTime, err = time.Parse(time.RFC3339, req.Before)
			if err != nil {
				respondError(w, http.StatusBadRequest, "invalid before parameter: must be RFC3339 datetime or duration like '7d'")
				return
			}
		}

		whereClause += fmt.Sprintf(" AND finished_at < $%d", argN)
		args = append(args, beforeTime)
		argN++
	}

	if len(req.IDs) > 0 {
		whereClause += fmt.Sprintf(" AND id = ANY($%d)", argN)
		args = append(args, req.IDs)
		argN++
	}

	// Execute the deletion
	result, err := a.db.Exec(r.Context(),
		`DELETE FROM sessions`+whereClause, args...)
	if err != nil {
		log.Printf("error: bulk deleting sessions: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to delete sessions")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"deleted_count": result.RowsAffected(),
	})
}

func (a *API) handleTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := a.checkTeamAccess(r.Context(), sessionID, r); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	// Check if client wants SSE for live sessions.
	if r.Header.Get("Accept") == "text/event-stream" || r.URL.Query().Get("stream") == "true" {
		a.streamTranscriptSSE(w, r, sessionID)
		return
	}

	// Static transcript fetch.
	var transcript json.RawMessage
	err := a.db.QueryRow(r.Context(),
		`SELECT transcript FROM sessions WHERE id = $1`, sessionID,
	).Scan(&transcript)
	if err != nil {
		respondError(w, http.StatusNotFound, "session or transcript not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"transcript": transcript,
	})
}

func (a *API) streamTranscriptSSE(w http.ResponseWriter, r *http.Request, sessionID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	log.Printf("sse: streaming transcript for session %s (client: %s)", sessionID, r.RemoteAddr)

	// Phase 1: Catch-up — send persisted events from database
	var transcript json.RawMessage
	_ = a.db.QueryRow(r.Context(),
		`SELECT COALESCE(transcript, '[]'::jsonb) FROM sessions WHERE id = $1`, sessionID,
	).Scan(&transcript)

	if transcript != nil {
		var events []json.RawMessage
		if json.Unmarshal(transcript, &events) == nil {
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
			flusher.Flush()
		}
	}

	// Phase 2: Subscribe to live NATS transcript events
	sub, err := a.dispatcher.nc.Subscribe(
		fmt.Sprintf("tasks.%s.transcript", sessionID),
		func(msg *nats.Msg) {
			fmt.Fprintf(w, "data: %s\n\n", msg.Data)
			flusher.Flush()
		},
	)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: {\"error\": \"subscribe failed\"}\n\n")
		flusher.Flush()
		return
	}
	defer sub.Unsubscribe()

	// Subscribe to status updates for session completion detection
	done := make(chan struct{})
	var doneOnce sync.Once
	statusSub, err := a.dispatcher.nc.Subscribe(
		fmt.Sprintf("tasks.%s.status", sessionID),
		func(msg *nats.Msg) {
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", msg.Data)
			flusher.Flush()

			var update StatusUpdate
			if json.Unmarshal(msg.Data, &update) == nil {
				if update.Status == "completed" || update.Status == "error" ||
					update.Status == "timeout" || update.Status == "cancelled" {
					fmt.Fprintf(w, "event: done\ndata: {\"status\": %q}\n\n", update.Status)
					flusher.Flush()
					doneOnce.Do(func() { close(done) })
				}
			}
		},
	)
	if err == nil {
		defer statusSub.Unsubscribe()
	}

	select {
	case <-r.Context().Done():
		log.Printf("sse: client disconnected for session %s (context done)", sessionID)
	case <-done:
		log.Printf("sse: session %s completed, closing SSE stream", sessionID)
	}
}

// --- Transcript/Status/ProxyLog Ingestion ---

func (a *API) handleAppendTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	// Validate session token for ingestion auth.
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		token = strings.TrimPrefix(token, "Bearer ")
	}
	if token != "" {
		var storedToken *string
		_ = a.db.QueryRow(r.Context(), `SELECT session_token FROM sessions WHERE id = $1`, sessionID).Scan(&storedToken)
		if storedToken == nil || token != *storedToken {
			respondError(w, http.StatusForbidden, "invalid session token")
			return
		}
	}

	var req struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Events) == 0 {
		respondError(w, http.StatusBadRequest, "events array is required and must not be empty")
		return
	}

	ctx := r.Context()

	// Marshal new events as a JSON array for atomic append.
	newEventsJSON, err := json.Marshal(req.Events)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to marshal events")
		return
	}

	// Atomic JSONB append — no read-modify-write race.
	_, err = a.db.Exec(ctx,
		`UPDATE sessions SET transcript = COALESCE(transcript, '[]'::jsonb) || $1::jsonb WHERE id = $2`,
		newEventsJSON, sessionID)
	if err != nil {
		log.Printf("error: updating transcript for session %s: %v", sessionID, err)
		respondError(w, http.StatusInternalServerError, "failed to update transcript")
		return
	}

	respondJSON(w, http.StatusOK, map[string]int{"appended": len(req.Events)})
}

func (a *API) handleUpdateStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	// Validate session token for ingestion auth.
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		token = strings.TrimPrefix(token, "Bearer ")
	}
	if token != "" {
		var storedToken *string
		_ = a.db.QueryRow(r.Context(), `SELECT session_token FROM sessions WHERE id = $1`, sessionID).Scan(&storedToken)
		if storedToken == nil || token != *storedToken {
			respondError(w, http.StatusForbidden, "invalid session token")
			return
		}
	}

	var req struct {
		Status    string              `json:"status"`
		ExitCode  *int                `json:"exit_code"`
		Artifacts []internal.Artifact `json:"artifacts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Status == "" {
		respondError(w, http.StatusBadRequest, "status is required")
		return
	}

	ctx := r.Context()
	now := time.Now().UTC()

	var artifactsJSON []byte
	if req.Artifacts != nil {
		var err error
		artifactsJSON, err = json.Marshal(req.Artifacts)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to marshal artifacts")
			return
		}
	}

	result, err := a.db.Exec(ctx,
		`UPDATE sessions SET outcome = $1, exit_code = $2, finished_at = $3, artifacts = $4 WHERE id = $5`,
		req.Status, req.ExitCode, now, artifactsJSON, sessionID)
	if err != nil {
		log.Printf("error: updating status for session %s: %v", sessionID, err)
		respondError(w, http.StatusInternalServerError, "failed to update status")
		return
	}
	if result.RowsAffected() == 0 {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"updated": true})
}

func (a *API) handleAppendProxyLog(w http.ResponseWriter, r *http.Request, sessionID string) {
	// Validate session token for ingestion auth.
	token := r.Header.Get("Authorization")
	if strings.HasPrefix(token, "Bearer ") {
		token = strings.TrimPrefix(token, "Bearer ")
	}
	if token != "" {
		var storedToken *string
		_ = a.db.QueryRow(r.Context(), `SELECT session_token FROM sessions WHERE id = $1`, sessionID).Scan(&storedToken)
		if storedToken == nil || token != *storedToken {
			respondError(w, http.StatusForbidden, "invalid session token")
			return
		}
	}

	var req struct {
		Entries []internal.ProxyLogEntry `json:"entries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Entries) == 0 {
		respondError(w, http.StatusBadRequest, "entries array is required and must not be empty")
		return
	}

	ctx := r.Context()

	// Marshal new entries as a JSON array for atomic append.
	newEntriesJSON, err := json.Marshal(req.Entries)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to marshal entries")
		return
	}

	// Atomic JSONB append — no read-modify-write race.
	_, err = a.db.Exec(ctx,
		`UPDATE sessions SET proxy_log = COALESCE(proxy_log, '[]'::jsonb) || $1::jsonb WHERE id = $2`,
		newEntriesJSON, sessionID)
	if err != nil {
		log.Printf("error: updating proxy log for session %s: %v", sessionID, err)
		respondError(w, http.StatusInternalServerError, "failed to update proxy log")
		return
	}

	respondJSON(w, http.StatusOK, map[string]int{"appended": len(req.Entries)})
}

func (a *API) handleGetProxyLog(w http.ResponseWriter, r *http.Request, sessionID string) {
	if err := a.checkTeamAccess(r.Context(), sessionID, r); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	var proxyLog json.RawMessage
	err := a.db.QueryRow(r.Context(),
		`SELECT COALESCE(proxy_log, '[]'::jsonb) FROM sessions WHERE id = $1`, sessionID,
	).Scan(&proxyLog)
	if err != nil {
		respondError(w, http.StatusNotFound, "session or proxy log not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"proxy_log":  proxyLog,
	})
}

// --- Providers ---

func (a *API) handleProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Build provider list from credential store.
	creds, err := a.credStore.ListDistinctProviders(r.Context())
	if err != nil {
		log.Printf("warning: listing providers from credentials: %v", err)
	}

	var providers []internal.Provider
	for _, c := range creds {
		providers = append(providers, internal.Provider{
			Name: c.Name,
			Type: c.Provider,
		})
	}

	// Fall back to env-based defaults when no credentials exist.
	if len(providers) == 0 {
		providers = defaultProviders()
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"providers": providers,
	})
}

// --- Database queries ---

// parseEventContext extracts trigger context from the prompt's event JSON
func parseEventContext(prompt string) string {
	// Look for event JSON at the end of the prompt: [event: {...}] or [webhook: {...}]
	eventPattern := []string{`\[event:\s*({[^}]+})\]`, `\[webhook:\s*({[^}]+})\]`}

	for _, pattern := range eventPattern {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(prompt); len(matches) > 1 {
			// Parse the JSON
			var eventData map[string]interface{}
			if err := json.Unmarshal([]byte(matches[1]), &eventData); err != nil {
				continue
			}

			// Extract GitHub issue number
			if issueNum, ok := eventData["GITHUB_ISSUE_NUMBER"].(string); ok && issueNum != "" {
				return fmt.Sprintf("Issue #%s", issueNum)
			}

			// Extract GitHub PR number
			if prNum, ok := eventData["GITHUB_PR_NUMBER"].(string); ok && prNum != "" {
				return fmt.Sprintf("PR #%s", prNum)
			}

			// Extract GitHub event type for other events
			if eventType, ok := eventData["GITHUB_EVENT"].(string); ok && eventType != "" {
				if repo, ok := eventData["GITHUB_REPO"].(string); ok && repo != "" {
					return fmt.Sprintf("%s: %s", eventType, repo)
				}
				return eventType
			}

			// Generic fallback
			return "GitHub Event"
		}
	}

	// No event context found
	return "Manual"
}

func (a *API) listSessions(ctx context.Context, status, repo, since, until, teamID string, page, perPage int) ([]internal.Session, int, error) {
	whereClause := " WHERE 1=1"
	args := []any{}
	argN := 1

	if teamID != "" {
		whereClause += fmt.Sprintf(" AND s.team_id = $%d", argN)
		args = append(args, teamID)
		argN++
	}
	if status != "" {
		statuses := strings.Split(status, ",")
		if len(statuses) == 1 {
			whereClause += fmt.Sprintf(" AND s.outcome = $%d", argN)
			args = append(args, status)
			argN++
		} else {
			placeholders := make([]string, len(statuses))
			for i, s := range statuses {
				placeholders[i] = fmt.Sprintf("$%d", argN)
				args = append(args, s)
				argN++
			}
			whereClause += " AND s.outcome IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	if repo != "" {
		whereClause += fmt.Sprintf(" AND s.prompt ILIKE '%%' || $%d || '%%'", argN)
		args = append(args, repo)
		argN++
	}
	if since != "" {
		whereClause += fmt.Sprintf(" AND s.started_at >= $%d", argN)
		args = append(args, since)
		argN++
	}
	if until != "" {
		whereClause += fmt.Sprintf(" AND s.finished_at <= $%d", argN)
		args = append(args, until)
		argN++
	}

	// Count total matching sessions
	countQuery := `SELECT COUNT(*) FROM sessions s` + whereClause
	countArgs := make([]any, len(args))
	copy(countArgs, args)

	var total int
	err := a.db.QueryRow(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Main query with pagination - include LEFT JOIN as fallback for old sessions
	query := `SELECT s.id, s.task_id, s.submitter, s.prompt, s.scope, s.provider, s.outcome, s.started_at, s.finished_at, s.exit_code, s.artifacts, s.parent_id,
		COALESCE(s.task_name, td.name, '') as task_name,
		COALESCE(s.trigger_type, '') as trigger_type,
		COALESCE(s.trigger_ref, '') as trigger_ref,
		COALESCE(s.repo, '') as repo
		FROM sessions s
		LEFT JOIN schedules sc ON s.prompt LIKE '%[' || sc.source_key || ']%' AND sc.source_key IS NOT NULL AND sc.source_key != ''
		LEFT JOIN agent_definitions td ON sc.source_key = td.source_key` +
		whereClause + " ORDER BY s.started_at DESC"

	offset := (page - 1) * perPage
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argN, argN+1)
	args = append(args, perPage, offset)

	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessions []internal.Session
	for rows.Next() {
		var s internal.Session
		var scopeJSON, artifactsJSON []byte
		var finishedAt *time.Time
		var exitCode *int
		var parentID *string
		var taskName, triggerType, triggerRef, repo string

		if err := rows.Scan(&s.ID, &s.TaskID, &s.Submitter, &s.Prompt,
			&scopeJSON, &s.Provider, &s.Status, &s.StartedAt, &finishedAt,
			&exitCode, &artifactsJSON, &parentID, &taskName, &triggerType, &triggerRef, &repo); err != nil {
			return nil, 0, err
		}

		if scopeJSON != nil {
			if err := json.Unmarshal(scopeJSON, &s.Scope); err != nil {
				log.Printf("warning: failed to unmarshal scope JSON for session %s: %v", s.ID, err)
			}
		}
		if finishedAt != nil {
			s.FinishedAt = finishedAt
			s.Duration = finishedAt.Sub(s.StartedAt).String()
		}
		s.ExitCode = exitCode
		if artifactsJSON != nil {
			if err := json.Unmarshal(artifactsJSON, &s.Artifacts); err != nil {
				log.Printf("warning: failed to unmarshal artifacts JSON for session %s: %v", s.ID, err)
			}
		}
		if parentID != nil {
			s.ParentID = *parentID
		}
		s.Repo = repo

		// Set task name with fallback
		if taskName != "" {
			s.TaskName = taskName
		} else {
			s.TaskName = "Manual Session"
		}

		// Set trigger type and ref from stored metadata
		s.TriggerType = triggerType
		s.TriggerRef = triggerRef

		// Parse trigger context from prompt as fallback for old sessions
		if triggerType != "" {
			s.TriggerContext = triggerType
			if triggerRef != "" {
				s.TriggerContext = triggerType + ": " + triggerRef
			}
		} else {
			s.TriggerContext = parseEventContext(s.Prompt)
		}

		sessions = append(sessions, s)
	}

	if sessions == nil {
		sessions = []internal.Session{}
	}

	return sessions, total, rows.Err()
}

func (a *API) getSession(ctx context.Context, id string) (*internal.Session, error) {
	var s internal.Session
	var scopeJSON, artifactsJSON []byte
	var finishedAt *time.Time
	var exitCode *int
	var parentID *string
	var taskName, triggerType, triggerRef, repo string

	err := a.db.QueryRow(ctx,
		`SELECT s.id, s.task_id, s.submitter, s.prompt, s.scope, s.provider, s.outcome, s.started_at, s.finished_at, s.exit_code, s.artifacts, s.parent_id,
		COALESCE(s.task_name, td.name, '') as task_name,
		COALESCE(s.trigger_type, '') as trigger_type,
		COALESCE(s.trigger_ref, '') as trigger_ref,
		COALESCE(s.repo, '') as repo
		FROM sessions s
		LEFT JOIN schedules sc ON s.prompt LIKE '%[' || sc.source_key || ']%' AND sc.source_key IS NOT NULL AND sc.source_key != ''
		LEFT JOIN agent_definitions td ON sc.source_key = td.source_key
		WHERE s.id = $1`, id,
	).Scan(&s.ID, &s.TaskID, &s.Submitter, &s.Prompt,
		&scopeJSON, &s.Provider, &s.Status, &s.StartedAt, &finishedAt,
		&exitCode, &artifactsJSON, &parentID, &taskName, &triggerType, &triggerRef, &repo)
	if err != nil {
		return nil, err
	}

	if scopeJSON != nil {
		if err := json.Unmarshal(scopeJSON, &s.Scope); err != nil {
			log.Printf("warning: failed to unmarshal scope JSON for session %s: %v", s.ID, err)
		}
	}
	if finishedAt != nil {
		s.FinishedAt = finishedAt
		s.Duration = finishedAt.Sub(s.StartedAt).String()
	}
	s.ExitCode = exitCode
	if artifactsJSON != nil {
		if err := json.Unmarshal(artifactsJSON, &s.Artifacts); err != nil {
			log.Printf("warning: failed to unmarshal artifacts JSON for session %s: %v", s.ID, err)
		}
	}
	if parentID != nil {
		s.ParentID = *parentID
	}
	s.Repo = repo

	// Set task name with fallback
	if taskName != "" {
		s.TaskName = taskName
	} else {
		s.TaskName = "Manual Session"
	}

	// Set trigger type and ref from stored metadata
	s.TriggerType = triggerType
	s.TriggerRef = triggerRef

	// Parse trigger context from prompt as fallback for old sessions
	if triggerType != "" {
		s.TriggerContext = triggerType
		if triggerRef != "" {
			s.TriggerContext = triggerType + ": " + triggerRef
		}
	} else {
		s.TriggerContext = parseEventContext(s.Prompt)
	}

	return &s, nil
}

// --- Schedules ---

func (a *API) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — schedules are managed via YAML")
		return
	}

	teamID := getActiveTeamID(r)

	schedules, err := a.scheduler.ListSchedules(r.Context(), teamID)
	if err != nil {
		log.Printf("error: listing schedules: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list schedules")
		return
	}

	// Annotate schedules with repo_disabled by joining through agent definitions.
	disabledRepos := a.buildDisabledReposMap(r.Context(), teamID)
	if len(disabledRepos) > 0 {
		// Build source_key -> source_repo map from agent definitions.
		defs, defErr := a.defStore.ListAgentDefinitions(r.Context(), teamID)
		if defErr == nil {
			sourceKeyToRepo := make(map[string]string)
			for _, d := range defs {
				if d.SourceKey != "" {
					sourceKeyToRepo[d.SourceKey] = d.SourceRepo
				}
			}
			for i := range schedules {
				if sourceRepo, ok := sourceKeyToRepo[schedules[i].SourceKey]; ok {
					if disabledRepos[sourceRepo] {
						schedules[i].RepoDisabled = true
					}
				}
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"schedules": schedules,
		"count":     len(schedules),
	})
}

func (a *API) handleScheduleByID(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/schedules/{id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/schedules/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		respondError(w, http.StatusBadRequest, "schedule id required")
		return
	}

	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — schedules are managed via YAML")
		return
	}

	teamID := getActiveTeamID(r)

	sched, err := a.scheduler.GetSchedule(r.Context(), id, teamID)
	if err != nil {
		respondError(w, http.StatusNotFound, "schedule not found")
		return
	}
	respondJSON(w, http.StatusOK, sched)
}

// --- Credentials ---

func (a *API) handleCredentials(w http.ResponseWriter, r *http.Request) {
	teamID := getActiveTeamID(r)

	switch r.Method {
	case http.MethodGet:
		creds, err := a.credStore.ListCredentials(r.Context(), teamID)
		if err != nil {
			log.Printf("error: listing credentials: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list credentials")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"credentials": creds,
			"count":       len(creds),
		})
	case http.MethodPost:
		var req struct {
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			AuthType   string `json:"auth_type"`
			Credential string `json:"credential"`
			ProjectID  string `json:"project_id"`
			Region     string `json:"region"`
			APIHost    string `json:"api_host,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.Name == "" || req.Provider == "" || req.AuthType == "" || req.Credential == "" {
			respondError(w, http.StatusBadRequest, "name, provider, auth_type, and credential are required")
			return
		}
		// Enforce one LLM credential per team.
		if ProviderCategory(req.Provider) == "llm" {
			existing, _ := a.credStore.ListCredentials(r.Context(), teamID)
			for _, c := range existing {
				if ProviderCategory(c.Provider) == "llm" {
					respondJSON(w, http.StatusConflict, map[string]any{
						"error":               "you already have an LLM credential configured",
						"existing_credential": c.Name,
						"existing_provider":   c.Provider,
						"existing_id":         c.ID,
					})
					return
				}
			}
		}
		cred := Credential{
			Name:      req.Name,
			Provider:  req.Provider,
			AuthType:  req.AuthType,
			ProjectID: req.ProjectID,
			Region:    req.Region,
			APIHost:   req.APIHost,
		}
		if err := a.credStore.CreateCredential(r.Context(), &cred, []byte(req.Credential), teamID); err != nil {
			log.Printf("error: creating credential: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to create credential: "+err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, cred)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleCredentialByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/credentials/")
	if id == "" {
		respondError(w, http.StatusBadRequest, "credential id required")
		return
	}

	teamID := getActiveTeamID(r)

	switch r.Method {
	case http.MethodGet:
		cred, err := a.credStore.GetCredential(r.Context(), id, teamID)
		if err != nil {
			respondError(w, http.StatusNotFound, "credential not found")
			return
		}
		respondJSON(w, http.StatusOK, cred)
	case http.MethodDelete:
		if err := a.credStore.DeleteCredential(r.Context(), id, teamID); err != nil {
			log.Printf("error: deleting credential %s: %v", id, err)
			respondError(w, http.StatusNotFound, "credential not found")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		SessionID     string `json:"session_id"`
		RefreshSecret string `json:"refresh_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.SessionID == "" || req.RefreshSecret == "" {
		respondError(w, http.StatusBadRequest, "session_id and refresh_secret are required")
		return
	}

	result, err := a.credStore.AcquireTokenBySessionID(r.Context(), req.SessionID)
	if err != nil {
		log.Printf("error: token refresh for session %s: %v", req.SessionID, err)
		respondError(w, http.StatusInternalServerError, "failed to acquire token: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, result)
}

// getActiveTeamID returns the active team ID from the X-Alcove-Team-ID header.
func getActiveTeamID(r *http.Request) string {
	return r.Header.Get("X-Alcove-Team-ID")
}

// checkTeamAccess verifies the session belongs to the active team.
func (a *API) checkTeamAccess(ctx context.Context, sessionID string, r *http.Request) error {
	teamID := getActiveTeamID(r)
	if teamID == "" {
		return fmt.Errorf("no team context")
	}
	var sessionTeamID string
	err := a.db.QueryRow(ctx, `SELECT team_id FROM sessions WHERE id = $1`, sessionID).Scan(&sessionTeamID)
	if err != nil {
		return fmt.Errorf("session not found")
	}
	if sessionTeamID != teamID {
		// Also allow admins.
		if r.Header.Get("X-Alcove-Admin") == "true" {
			return nil
		}
		return fmt.Errorf("access denied")
	}
	return nil
}

// checkOwnership verifies the requesting user owns the session (legacy, delegates to team access).
func (a *API) checkOwnership(ctx context.Context, sessionID, user string) error {
	if user == "" {
		return fmt.Errorf("no user context")
	}
	var submitter string
	err := a.db.QueryRow(ctx, `SELECT submitter FROM sessions WHERE id = $1`, sessionID).Scan(&submitter)
	if err != nil {
		return fmt.Errorf("session not found")
	}
	if submitter != user {
		return fmt.Errorf("access denied")
	}
	return nil
}

// --- Tools ---

func (a *API) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — tools are managed via YAML")
		return
	}

	teamID := getActiveTeamID(r)

	tools, err := a.toolStore.ListTools(r.Context(), teamID)
	if err != nil {
		log.Printf("error: listing tools: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list tools")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"tools": tools,
		"count": len(tools),
	})
}

func (a *API) handleToolByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — tools are managed via YAML")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/v1/tools/")
	if name == "" {
		respondError(w, http.StatusBadRequest, "tool name required")
		return
	}

	teamID := getActiveTeamID(r)

	tool, err := a.toolStore.GetTool(r.Context(), name, teamID)
	if err != nil {
		respondError(w, http.StatusNotFound, "tool not found")
		return
	}
	respondJSON(w, http.StatusOK, tool)
}

// --- Security Profiles ---

func (a *API) handleSecurityProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — security profiles are managed via YAML")
		return
	}

	teamID := getActiveTeamID(r)

	profiles, err := a.profileStore.ListProfiles(r.Context(), teamID)
	if err != nil {
		log.Printf("error: listing profiles: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list profiles")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"profiles": profiles,
		"count":    len(profiles),
	})
}

func (a *API) handleSecurityProfileByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed — security profiles are managed via YAML")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/api/v1/security-profiles/")
	if name == "" {
		respondError(w, http.StatusBadRequest, "profile name required")
		return
	}

	teamID := getActiveTeamID(r)

	profile, err := a.profileStore.GetProfile(r.Context(), name, teamID)
	if err != nil {
		respondError(w, http.StatusNotFound, "profile not found")
		return
	}
	respondJSON(w, http.StatusOK, profile)
}

// --- Admin Settings ---

func (a *API) handleAdminSettingsLLM(w http.ResponseWriter, r *http.Request) {
	// Admin check.
	if r.Header.Get("X-Alcove-Admin") != "true" {
		respondError(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		eff := ResolveEffectiveLLM(a.cfg)
		respondJSON(w, http.StatusOK, eff)
	case http.MethodPut, http.MethodDelete:
		respondError(w, http.StatusMethodNotAllowed, "system LLM is configured via alcove.yaml")
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Agent Repos Settings ---

func (a *API) handleUserSettingsAgentRepos(w http.ResponseWriter, r *http.Request) {
	teamID := getActiveTeamID(r)
	if teamID == "" {
		// Fallback to user_settings for backward compatibility.
		username := r.Header.Get("X-Alcove-User")
		if username == "" {
			respondError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		switch r.Method {
		case http.MethodGet:
			repos, err := a.settingsStore.GetUserAgentRepos(r.Context(), username)
			if err != nil {
				repos = []SkillRepo{}
			}
			respondJSON(w, http.StatusOK, map[string]any{"repos": repos})
		case http.MethodPut:
			var req struct {
				Repos []SkillRepo `json:"repos"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			if req.Repos == nil {
				req.Repos = []SkillRepo{}
			}
			if err := a.settingsStore.SetUserAgentRepos(r.Context(), username, req.Repos); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to save agent repos")
				return
			}
			go a.syncer.SyncAll(context.Background())
			respondJSON(w, http.StatusOK, map[string]any{"repos": req.Repos})
		default:
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		repos, err := a.teamStore.GetTeamAgentRepos(r.Context(), teamID)
		if err != nil {
			repos = []SkillRepo{}
		}
		respondJSON(w, http.StatusOK, map[string]any{"repos": repos})
	case http.MethodPut:
		var req struct {
			Repos []SkillRepo `json:"repos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.Repos == nil {
			req.Repos = []SkillRepo{}
		}
		if err := a.teamStore.SetTeamAgentRepos(r.Context(), teamID, req.Repos); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save agent repos")
			return
		}
		go a.syncer.SyncAll(context.Background())
		respondJSON(w, http.StatusOK, map[string]any{"repos": req.Repos})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Agent Repo Validation ---

func (a *API) handleAgentRepoValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var repo SkillRepo
	if err := json.NewDecoder(r.Body).Decode(&repo); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if repo.URL == "" {
		respondError(w, http.StatusBadRequest, "url is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	names, err := a.syncer.ValidateRepo(ctx, repo)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"valid": true, "agent_definition_count": len(names), "agent_definitions": names})
}

// --- Agent Definitions ---

func (a *API) handleAgentDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	teamID := getActiveTeamID(r)
	defs, err := a.defStore.ListAgentDefinitions(r.Context(), teamID)
	if err != nil {
		log.Printf("error: listing agent definitions: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list agent definitions")
		return
	}

	// Fetch team's agent repos to check disabled status.
	disabledRepos := a.buildDisabledReposMap(r.Context(), teamID)

	// Annotate each agent definition with repo_disabled.
	for i := range defs {
		if disabledRepos[defs[i].SourceRepo] {
			defs[i].RepoDisabled = true
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"agent_definitions": defs,
		"count":            len(defs),
	})
}

func (a *API) handleAgentDefinitionsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if a.syncer == nil {
		respondError(w, http.StatusServiceUnavailable, "agent repo syncer not configured")
		return
	}

	if err := a.syncer.SyncAll(r.Context()); err != nil {
		log.Printf("error: manual agent sync: %v", err)
		respondError(w, http.StatusInternalServerError, "sync failed: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"synced": true})
}

func (a *API) handleAgentDefinitionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agent-definitions/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		respondError(w, http.StatusBadRequest, "agent definition id required")
		return
	}

	// Handle /api/v1/agent-definitions/{id}/run
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.handleAgentDefinitionRun(w, r, id)
		return
	}

	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	teamID := getActiveTeamID(r)
	def, err := a.defStore.GetAgentDefinition(r.Context(), id, teamID)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent definition not found")
		return
	}

	respondJSON(w, http.StatusOK, def)
}

func (a *API) handleAgentDefinitionRun(w http.ResponseWriter, r *http.Request, id string) {
	// Check system mode before dispatching.
	if mode, _ := a.settingsStore.GetSystemMode(r.Context()); mode == "paused" {
		respondError(w, http.StatusServiceUnavailable, "system is paused for maintenance — new sessions are not being accepted")
		return
	}

	submitter := r.Header.Get("X-Alcove-User")
	if submitter == "" {
		submitter = "anonymous"
	}

	teamID := getActiveTeamID(r)
	def, err := a.defStore.GetAgentDefinition(r.Context(), id, teamID)
	if err != nil {
		respondError(w, http.StatusNotFound, "agent definition not found")
		return
	}

	if def.SyncError != "" {
		respondError(w, http.StatusBadRequest, "agent definition has sync error: "+def.SyncError)
		return
	}

	req := def.ToTaskRequest()
	req.TaskName = def.Name
	req.TriggerType = "manual"
	session, err := a.dispatcher.DispatchTask(r.Context(), req, submitter, teamID)
	if err != nil {
		log.Printf("error: dispatching agent definition %s: %v", id, err)
		respondError(w, http.StatusInternalServerError, "failed to dispatch session: "+err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, session)
}

// --- Agent Templates ---

func (a *API) handleAgentTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to read templates")
		return
	}

	type tmpl struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		RawYAML     string `json:"raw_yaml"`
	}

	var templates []tmpl
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		data, err := fs.ReadFile(templateFS, "templates/"+entry.Name())
		if err != nil {
			continue
		}
		td, err := ParseTaskDefinition(data)
		if err != nil {
			// Include unparseable templates with filename as name.
			templates = append(templates, tmpl{
				Name:    entry.Name(),
				RawYAML: string(data),
			})
			continue
		}
		templates = append(templates, tmpl{
			Name:        td.Name,
			Description: td.Description,
			RawYAML:     string(data),
		})
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"templates": templates,
		"count":     len(templates),
	})
}

// --- Webhooks ---

func (a *API) handleWebhookGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx := r.Context()

	// Read the raw body for HMAC validation.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Validate HMAC signature.
	secret, err := a.settingsStore.GetWebhookSecret(ctx)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "webhook secret not configured")
		return
	}

	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if sigHeader == "" {
		respondError(w, http.StatusUnauthorized, "missing signature header")
		return
	}

	// Compute expected signature.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sigHeader), []byte(expectedSig)) {
		respondError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	// Check idempotency via X-GitHub-Delivery header.
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		deliveryID = fmt.Sprintf("unknown-%d", time.Now().UnixNano())
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		respondError(w, http.StatusBadRequest, "missing X-GitHub-Event header")
		return
	}

	// Parse the payload.
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	// Extract event info.
	action, _ := payload["action"].(string)
	repo := ""
	if repoObj, ok := payload["repository"].(map[string]any); ok {
		repo, _ = repoObj["full_name"].(string)
	}

	// Extract branch.
	branch := ""
	switch eventType {
	case "push":
		if ref, ok := payload["ref"].(string); ok {
			// ref is like "refs/heads/main"
			branch = strings.TrimPrefix(ref, "refs/heads/")
		}
	case "pull_request":
		if pr, ok := payload["pull_request"].(map[string]any); ok {
			if head, ok := pr["head"].(map[string]any); ok {
				branch, _ = head["ref"].(string)
			}
		}
	}

	// Extract labels from issue or pull_request.
	var labels []string
	if issue, ok := payload["issue"].(map[string]any); ok {
		if labelArr, ok := issue["labels"].([]any); ok {
			for _, lo := range labelArr {
				if lm, ok := lo.(map[string]any); ok {
					if name, ok := lm["name"].(string); ok {
						labels = append(labels, name)
					}
				}
			}
		}
	} else if pr, ok := payload["pull_request"].(map[string]any); ok {
		if labelArr, ok := pr["labels"].([]any); ok {
			for _, lo := range labelArr {
				if lm, ok := lo.(map[string]any); ok {
					if name, ok := lm["name"].(string); ok {
						labels = append(labels, name)
					}
				}
			}
		}
	}

	// Extract user from comment or issue.
	var users []string
	if comment, ok := payload["comment"].(map[string]any); ok {
		if user, ok := comment["user"].(map[string]any); ok {
			if login, ok := user["login"].(string); ok {
				users = append(users, login)
			}
		}
	} else if issue, ok := payload["issue"].(map[string]any); ok {
		if user, ok := issue["user"].(map[string]any); ok {
			if login, ok := user["login"].(string); ok {
				users = append(users, login)
			}
		}
	}

	// Extract additional info for dispatched tasks.
	sha := ""
	prNumber := ""
	issueNumber := ""
	switch eventType {
	case "push":
		if after, ok := payload["after"].(string); ok {
			sha = after
		}
	case "pull_request":
		if pr, ok := payload["pull_request"].(map[string]any); ok {
			if head, ok := pr["head"].(map[string]any); ok {
				if s, ok := head["sha"].(string); ok {
					sha = s
				}
			}
			if num, ok := pr["number"].(float64); ok {
				prNumber = fmt.Sprintf("%d", int(num))
			}
		}
	case "issues", "issue_comment":
		if issue, ok := payload["issue"].(map[string]any); ok {
			if num, ok := issue["number"].(float64); ok {
				issueNumber = fmt.Sprintf("%d", int(num))
			}
		}
	}

	// Record delivery for idempotency (INSERT ... ON CONFLICT DO NOTHING).
	result, err := a.db.Exec(ctx,
		`INSERT INTO webhook_deliveries (delivery_id, event_type, repo, action) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		deliveryID, eventType, repo, action)
	if err != nil {
		log.Printf("webhook: error recording delivery %s: %v", deliveryID, err)
		respondError(w, http.StatusInternalServerError, "failed to record delivery")
		return
	}
	if result.RowsAffected() == 0 {
		// Already processed.
		respondJSON(w, http.StatusOK, map[string]any{"matched": 0, "dispatched": 0, "duplicate": true})
		return
	}

	// Check system mode before dispatching.
	if mode, _ := a.settingsStore.GetSystemMode(ctx); mode == "paused" {
		respondJSON(w, http.StatusOK, map[string]any{"matched": 0, "dispatched": 0, "paused": true})
		return
	}

	// Query schedules with event triggers.
	rows, queryErr := a.db.Query(ctx, `
		SELECT id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, team_id, debug, trigger_type, event_config
		FROM schedules
		WHERE enabled = true
		  AND COALESCE(trigger_type, 'cron') IN ('event', 'cron-and-event')
		  AND event_config IS NOT NULL
	`)
	if queryErr != nil {
		log.Printf("webhook: error querying schedules: %v", queryErr)
		respondError(w, http.StatusInternalServerError, "failed to query schedules")
		return
	}
	defer rows.Close()

	matched := 0
	dispatched := 0

	for rows.Next() {
		var sched struct {
			ID          string
			Name        string
			Cron        string
			Prompt      string
			Repo        string
			Provider    string
			ScopePreset string
			Timeout     int
			Enabled     bool
			TeamID      string
			Debug       bool
			TriggerType string
			EventConfig []byte
		}

		if err := rows.Scan(
			&sched.ID, &sched.Name, &sched.Cron, &sched.Prompt,
			&sched.Repo, &sched.Provider, &sched.ScopePreset, &sched.Timeout,
			&sched.Enabled, &sched.TeamID, &sched.Debug, &sched.TriggerType, &sched.EventConfig,
		); err != nil {
			log.Printf("webhook: error scanning schedule: %v", err)
			continue
		}

		var trigger EventTrigger
		if err := json.Unmarshal(sched.EventConfig, &trigger); err != nil {
			log.Printf("webhook: error unmarshaling event_config for schedule %s: %v", sched.ID, err)
			continue
		}

		if trigger.GitHub == nil || !trigger.GitHub.Matches(eventType, action, repo, branch, labels, users) {
			continue
		}

		matched++

		// Build TaskRequest with webhook context as env vars.
		taskReq := TaskRequest{
			Prompt:      sched.Prompt,
			Repo:        sched.Repo,
			Provider:    sched.Provider,
			Timeout:     sched.Timeout,
			Debug:       sched.Debug,
			TaskName:    sched.Name,
			TriggerType: "webhook",
		}
		if issueNumber != "" {
			taskReq.TriggerRef = fmt.Sprintf("%s#%s", repo, issueNumber)
		} else if prNumber != "" {
			taskReq.TriggerRef = fmt.Sprintf("%s#%s", repo, prNumber)
		}

		session, err := a.dispatcher.DispatchTask(ctx, taskReq, "webhook", sched.TeamID)
		if err != nil {
			log.Printf("webhook: error dispatching schedule %s (%s): %v", sched.Name, sched.ID, err)
			continue
		}

		// Store webhook context as metadata on the session.
		webhookMeta := map[string]string{
			"GITHUB_EVENT":        eventType,
			"GITHUB_REPO":         repo,
			"GITHUB_REF":          branch,
			"GITHUB_SHA":          sha,
			"GITHUB_PR_NUMBER":    prNumber,
			"GITHUB_ISSUE_NUMBER": issueNumber,
		}
		metaJSON, _ := json.Marshal(webhookMeta)
		_, _ = a.db.Exec(ctx,
			`UPDATE sessions SET prompt = prompt || E'\n\n[webhook: ' || $1 || ']' WHERE id = $2`,
			string(metaJSON), session.ID)

		dispatched++
		log.Printf("webhook: dispatched schedule %s (%s) for %s %s/%s", sched.Name, sched.ID, eventType, repo, action)
	}

	// Update delivery record with match count.
	_, _ = a.db.Exec(ctx,
		`UPDATE webhook_deliveries SET matched_schedules = $1 WHERE delivery_id = $2`,
		matched, deliveryID)

	respondJSON(w, http.StatusOK, map[string]any{"matched": matched, "dispatched": dispatched})
}

// --- Admin Settings: System State ---

func (a *API) handleSystemState(w http.ResponseWriter, r *http.Request) {
	// Admin-only.
	if r.Header.Get("X-Alcove-Admin") != "true" {
		respondError(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		mode, _ := a.settingsStore.GetSystemMode(r.Context())

		// Count running sessions.
		var running int
		_ = a.db.QueryRow(r.Context(), "SELECT COUNT(*) FROM sessions WHERE outcome = 'running'").Scan(&running)

		respondJSON(w, http.StatusOK, map[string]any{
			"mode":             mode,
			"running_sessions": running,
		})

	case http.MethodPut:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Mode != "active" && req.Mode != "paused" {
			respondError(w, http.StatusBadRequest, "mode must be 'active' or 'paused'")
			return
		}

		if err := a.settingsStore.SetSystemMode(r.Context(), req.Mode); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to set system mode")
			return
		}

		log.Printf("system mode changed to %s by admin", req.Mode)
		respondJSON(w, http.StatusOK, map[string]any{"mode": req.Mode})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Admin Settings: Webhook ---

func (a *API) handleAdminSettingsWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Alcove-Admin") != "true" {
		respondError(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		_, err := a.settingsStore.GetWebhookSecret(r.Context())
		configured := err == nil
		respondJSON(w, http.StatusOK, map[string]any{
			"configured": configured,
			"url":        "/api/v1/webhooks/github",
		})
	case http.MethodPut:
		var req struct {
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		// Auto-generate if not provided.
		if req.Secret == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to generate secret")
				return
			}
			req.Secret = hex.EncodeToString(b)
		}

		if err := a.settingsStore.SetWebhookSecret(r.Context(), req.Secret); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save webhook secret")
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"configured": true,
			"secret":     req.Secret,
			"url":        "/api/v1/webhooks/github",
		})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- TBR Identity Associations ---

func (a *API) handleTBRAssociations(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Only supported with rh-identity backend
	if a.cfg.AuthBackend != "rh-identity" {
		respondError(w, http.StatusBadRequest, "TBR associations only supported with rh-identity backend")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List user's TBR associations
		rhStore := a.authStore.(*auth.RHIdentityStore)
		associations, err := rhStore.GetTBRAssociations(r.Context(), username)
		if err != nil {
			log.Printf("error fetching TBR associations for user %s: %v", username, err)
			respondError(w, http.StatusInternalServerError, "failed to fetch associations")
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"associations": associations,
		})

	case http.MethodPost:
		// Create new TBR association
		var req struct {
			TBROrgID    string `json:"tbr_org_id"`
			TBRUsername string `json:"tbr_username"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if req.TBROrgID == "" || req.TBRUsername == "" {
			respondError(w, http.StatusBadRequest, "tbr_org_id and tbr_username are required")
			return
		}

		rhStore := a.authStore.(*auth.RHIdentityStore)
		association, err := rhStore.CreateTBRAssociation(r.Context(), username, req.TBROrgID, req.TBRUsername)
		if err != nil {
			log.Printf("error creating TBR association for user %s: %v", username, err)
			respondError(w, http.StatusConflict, err.Error())
			return
		}

		respondJSON(w, http.StatusCreated, association)

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleTBRAssociationByID(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Only supported with rh-identity backend
	if a.cfg.AuthBackend != "rh-identity" {
		respondError(w, http.StatusBadRequest, "TBR associations only supported with rh-identity backend")
		return
	}

	// Extract association ID from URL path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/tbr-associations/")
	if path == "" {
		respondError(w, http.StatusBadRequest, "association ID required")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		// Delete TBR association (user must own it)
		rhStore := a.authStore.(*auth.RHIdentityStore)
		if err := rhStore.DeleteTBRAssociation(r.Context(), username, path); err != nil {
			log.Printf("error deleting TBR association %s for user %s: %v", path, username, err)
			respondError(w, http.StatusNotFound, err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"deleted": true,
		})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handlePersonalAPITokens(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Only supported with postgres backend
	if a.cfg.AuthBackend != "postgres" {
		respondError(w, http.StatusBadRequest, "personal API tokens only supported with postgres backend")
		return
	}

	pgStore, ok := a.authStore.(*auth.PgStore)
	if !ok {
		respondError(w, http.StatusInternalServerError, "postgres auth store not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// List user's personal API tokens
		tokens, err := pgStore.ListPersonalAPITokens(r.Context(), username)
		if err != nil {
			log.Printf("error fetching personal API tokens for user %s: %v", username, err)
			respondError(w, http.StatusInternalServerError, "failed to fetch tokens")
			return
		}

		respondJSON(w, http.StatusOK, tokens)

	case http.MethodPost:
		// Create new personal API token
		var req struct {
			Name string `json:"name"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if strings.TrimSpace(req.Name) == "" {
			respondError(w, http.StatusBadRequest, "name is required")
			return
		}

		response, err := pgStore.CreatePersonalAPIToken(r.Context(), username, strings.TrimSpace(req.Name))
		if err != nil {
			log.Printf("error creating personal API token for user %s: %v", username, err)
			respondError(w, http.StatusInternalServerError, "failed to create token")
			return
		}

		respondJSON(w, http.StatusCreated, response)

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handlePersonalAPITokenByID(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	// Only supported with postgres backend
	if a.cfg.AuthBackend != "postgres" {
		respondError(w, http.StatusBadRequest, "personal API tokens only supported with postgres backend")
		return
	}

	pgStore, ok := a.authStore.(*auth.PgStore)
	if !ok {
		respondError(w, http.StatusInternalServerError, "postgres auth store not available")
		return
	}

	// Extract token ID from URL path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/api-tokens/")
	if path == "" {
		respondError(w, http.StatusBadRequest, "token ID required")
		return
	}

	switch r.Method {
	case http.MethodDelete:
		// Delete personal API token (user must own it)
		if err := pgStore.DeletePersonalAPIToken(r.Context(), username, path); err != nil {
			log.Printf("error deleting personal API token %s for user %s: %v", path, username, err)
			respondError(w, http.StatusNotFound, "token not found or not owned by user")
			return
		}

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"deleted": true,
		})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// buildDisabledReposMap returns a set of repo URLs that are disabled for the given user.
func (a *API) buildDisabledReposMap(ctx context.Context, teamID string) map[string]bool {
	// Try team settings first, fall back to user_settings for backward compat.
	if teamID != "" && a.teamStore != nil {
		agentRepos, err := a.teamStore.GetTeamAgentRepos(ctx, teamID)
		if err == nil {
			disabledRepos := make(map[string]bool)
			for _, repo := range agentRepos {
				if !repo.IsEnabled() {
					disabledRepos[repo.URL] = true
				}
			}
			return disabledRepos
		}
	}
	return nil
}

// --- Workflows ---

func (a *API) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	teamID := getActiveTeamID(r)

	workflows, err := a.workflowEngine.workflowStore.ListWorkflows(r.Context(), teamID)
	if err != nil {
		log.Printf("error listing workflows: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"workflows": workflows,
		"count":     len(workflows),
	})
}

func (a *API) handleWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	teamID := getActiveTeamID(r)

	status := r.URL.Query().Get("status")

	runs, err := a.workflowEngine.ListWorkflowRuns(r.Context(), status, teamID)
	if err != nil {
		log.Printf("error listing workflow runs: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list workflow runs")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"workflow_runs": runs,
		"count":         len(runs),
	})
}

func (a *API) handleWorkflowRunByID(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/workflow-runs/{id} or /api/v1/workflow-runs/{id}/approve/{step_id} or /api/v1/workflow-runs/{id}/reject/{step_id}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workflow-runs/")
	parts := strings.SplitN(path, "/", 3)
	runID := parts[0]

	if runID == "" {
		respondError(w, http.StatusBadRequest, "workflow run id required")
		return
	}

	// Handle approve/reject actions
	if len(parts) >= 3 {
		action := parts[1]
		stepID := parts[2]

		if r.Method != http.MethodPost {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		switch action {
		case "approve":
			if err := a.workflowEngine.ApproveStep(r.Context(), runID, stepID); err != nil {
				log.Printf("error approving step %s in run %s: %v", stepID, runID, err)
				respondError(w, http.StatusBadRequest, err.Error())
				return
			}
			respondJSON(w, http.StatusOK, map[string]string{
				"status":  "approved",
				"run_id":  runID,
				"step_id": stepID,
			})
		case "reject":
			if err := a.workflowEngine.RejectStep(r.Context(), runID, stepID); err != nil {
				log.Printf("error rejecting step %s in run %s: %v", stepID, runID, err)
				respondError(w, http.StatusBadRequest, err.Error())
				return
			}
			respondJSON(w, http.StatusOK, map[string]string{
				"status":  "rejected",
				"run_id":  runID,
				"step_id": stepID,
			})
		default:
			respondError(w, http.StatusNotFound, "unknown action: "+action)
		}
		return
	}

	// GET /api/v1/workflow-runs/{id} — get run detail with all steps
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	run, steps, err := a.workflowEngine.GetWorkflowRunDetail(r.Context(), runID)
	if err != nil {
		log.Printf("error getting workflow run %s: %v", runID, err)
		respondError(w, http.StatusNotFound, "workflow run not found")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"workflow_run": run,
		"steps":        steps,
	})
}

// --- Bridge Actions ---

func (a *API) handleBridgeActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	schemas := ListBridgeActionSchemas()
	respondJSON(w, http.StatusOK, map[string]any{
		"actions": schemas,
		"count":   len(schemas),
	})
}

// --- Catalog ---

func (a *API) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	entries := LoadCatalog()

	// Build category counts
	categoryCounts := make(map[string]int)
	for _, e := range entries {
		categoryCounts[e.Category]++
	}
	type categoryInfo struct {
		ID    string `json:"id"`
		Count int    `json:"count"`
	}
	var categories []categoryInfo
	for cat, count := range categoryCounts {
		categories = append(categories, categoryInfo{ID: cat, Count: count})
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"entries":    entries,
		"count":      len(entries),
		"categories": categories,
	})
}

// --- Helpers ---

func respondJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func respondError(w http.ResponseWriter, code int, msg string) {
	respondJSON(w, code, map[string]string{"error": msg})
}
