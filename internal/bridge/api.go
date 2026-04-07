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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

//go:embed templates/*.yml
var templateFS embed.FS

// API holds the HTTP handlers for the Bridge REST API.
type API struct {
	dispatcher    *Dispatcher
	db            *pgxpool.Pool
	cfg           *Config
	scheduler     *Scheduler
	credStore     *CredentialStore
	toolStore     *ToolStore
	profileStore  *ProfileStore
	settingsStore *SettingsStore
	llm           *BridgeLLM
	defStore      *TaskDefStore
	syncer        *TaskRepoSyncer
}

// NewAPI creates the API handler set.
func NewAPI(dispatcher *Dispatcher, db *pgxpool.Pool, cfg *Config, scheduler *Scheduler, credStore *CredentialStore, toolStore *ToolStore, profileStore *ProfileStore, settingsStore *SettingsStore, llm *BridgeLLM, defStore *TaskDefStore, syncer *TaskRepoSyncer) *API {
	return &API{
		dispatcher:    dispatcher,
		db:            db,
		cfg:           cfg,
		scheduler:     scheduler,
		credStore:     credStore,
		toolStore:     toolStore,
		profileStore:  profileStore,
		settingsStore: settingsStore,
		llm:           llm,
		defStore:      defStore,
		syncer:        syncer,
	}
}

// RegisterRoutes registers all API routes on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/health", a.handleHealth)
	mux.HandleFunc("/api/v1/tasks", a.handleTasks)
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
	mux.HandleFunc("/api/v1/security-profiles/build", a.handleSecurityProfileBuild)
	mux.HandleFunc("/api/v1/security-profiles/", a.handleSecurityProfileByID)
	mux.HandleFunc("/api/v1/internal/token-refresh", a.handleTokenRefresh)
	mux.HandleFunc("/api/v1/admin/settings/llm", a.handleAdminSettingsLLM)
	mux.HandleFunc("/api/v1/admin/settings/skill-repos", a.handleAdminSettingsSkillRepos)
	mux.HandleFunc("/api/v1/user/settings/skill-repos", a.handleUserSettingsSkillRepos)
	mux.HandleFunc("/api/v1/user/settings/task-repos", a.handleUserSettingsTaskRepos)
	mux.HandleFunc("/api/v1/task-repos/validate", a.handleTaskRepoValidate)
	mux.HandleFunc("/api/v1/task-definitions", a.handleTaskDefinitions)
	mux.HandleFunc("/api/v1/task-definitions/sync", a.handleTaskDefinitionsSync)
	mux.HandleFunc("/api/v1/task-definitions/", a.handleTaskDefinitionByID)
	mux.HandleFunc("/api/v1/task-templates", a.handleTaskTemplates)
	mux.HandleFunc("/api/v1/webhooks/github", a.handleWebhookGitHub)
	mux.HandleFunc("/api/v1/admin/settings/webhook", a.handleAdminSettingsWebhook)
	mux.HandleFunc("/api/v1/system-info", a.handleSystemInfo)
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

// --- Tasks ---

func (a *API) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
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

	session, err := a.dispatcher.DispatchTask(r.Context(), req, submitter)
	if err != nil {
		log.Printf("error: dispatch failed: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to dispatch task: "+err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, session)
}

// --- Sessions ---

func (a *API) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	query := r.URL.Query()
	status := query.Get("status")
	repo := query.Get("repo")
	since := query.Get("since")
	until := query.Get("until")
	user := r.Header.Get("X-Alcove-User")

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

	sessions, total, err := a.listSessions(r.Context(), status, repo, since, until, user, page, perPage)
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

	user := r.Header.Get("X-Alcove-User")

	if len(parts) == 2 {
		switch parts[1] {
		case "transcript":
			if r.Method == http.MethodPost {
				a.handleAppendTranscript(w, r, sessionID)
			} else {
				a.handleTranscript(w, r, sessionID, user)
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
				a.handleGetProxyLog(w, r, sessionID, user)
			default:
				respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		a.handleGetSession(w, r, sessionID, user)
	case http.MethodDelete:
		a.handleCancelSession(w, r, sessionID, user)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleGetSession(w http.ResponseWriter, r *http.Request, sessionID string, user string) {
	if err := a.checkOwnership(r.Context(), sessionID, user); err != nil {
		respondError(w, http.StatusForbidden, "access denied")
		return
	}

	session, err := a.getSession(r.Context(), sessionID)
	if err != nil {
		respondError(w, http.StatusNotFound, "session not found")
		return
	}

	// Also fetch transcript and proxy log.
	type sessionDetail struct {
		internal.Session
		Transcript json.RawMessage `json:"transcript,omitempty"`
		ProxyLog   json.RawMessage `json:"proxy_log,omitempty"`
	}

	detail := sessionDetail{Session: *session}

	var transcript, proxyLog []byte
	_ = a.db.QueryRow(r.Context(),
		`SELECT transcript, proxy_log FROM sessions WHERE id = $1`, sessionID,
	).Scan(&transcript, &proxyLog)

	if transcript != nil {
		detail.Transcript = transcript
	}
	if proxyLog != nil {
		detail.ProxyLog = proxyLog
	}

	respondJSON(w, http.StatusOK, detail)
}

func (a *API) handleCancelSession(w http.ResponseWriter, r *http.Request, sessionID string, user string) {
	if err := a.checkOwnership(r.Context(), sessionID, user); err != nil {
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

func (a *API) handleTranscript(w http.ResponseWriter, r *http.Request, sessionID string, user string) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if err := a.checkOwnership(r.Context(), sessionID, user); err != nil {
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
	case <-done:
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

func (a *API) handleGetProxyLog(w http.ResponseWriter, r *http.Request, sessionID string, user string) {
	if err := a.checkOwnership(r.Context(), sessionID, user); err != nil {
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

func (a *API) listSessions(ctx context.Context, status, repo, since, until, submitter string, page, perPage int) ([]internal.Session, int, error) {
	whereClause := " WHERE 1=1"
	args := []any{}
	argN := 1

	if submitter != "" {
		whereClause += fmt.Sprintf(" AND submitter = $%d", argN)
		args = append(args, submitter)
		argN++
	}
	if status != "" {
		whereClause += fmt.Sprintf(" AND outcome = $%d", argN)
		args = append(args, status)
		argN++
	}
	if repo != "" {
		whereClause += fmt.Sprintf(" AND prompt ILIKE '%%' || $%d || '%%'", argN)
		args = append(args, repo)
		argN++
	}
	if since != "" {
		whereClause += fmt.Sprintf(" AND started_at >= $%d", argN)
		args = append(args, since)
		argN++
	}
	if until != "" {
		whereClause += fmt.Sprintf(" AND started_at <= $%d", argN)
		args = append(args, until)
		argN++
	}

	// Count total matching sessions
	countQuery := `SELECT COUNT(*) FROM sessions` + whereClause
	countArgs := make([]any, len(args))
	copy(countArgs, args)

	var total int
	err := a.db.QueryRow(ctx, countQuery, countArgs...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	// Main query with pagination
	query := `SELECT id, task_id, submitter, prompt, scope, provider, outcome, started_at, finished_at, exit_code, artifacts, parent_id
		FROM sessions` + whereClause + " ORDER BY started_at DESC"

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

		if err := rows.Scan(&s.ID, &s.TaskID, &s.Submitter, &s.Prompt,
			&scopeJSON, &s.Provider, &s.Status, &s.StartedAt, &finishedAt,
			&exitCode, &artifactsJSON, &parentID); err != nil {
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

	err := a.db.QueryRow(ctx,
		`SELECT id, task_id, submitter, prompt, scope, provider, outcome, started_at, finished_at, exit_code, artifacts, parent_id
		FROM sessions WHERE id = $1`, id,
	).Scan(&s.ID, &s.TaskID, &s.Submitter, &s.Prompt,
		&scopeJSON, &s.Provider, &s.Status, &s.StartedAt, &finishedAt,
		&exitCode, &artifactsJSON, &parentID)
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

	return &s, nil
}

// --- Schedules ---

func (a *API) handleSchedules(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		schedules, err := a.scheduler.ListSchedules(r.Context(), user)
		if err != nil {
			log.Printf("error: listing schedules: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list schedules")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"schedules": schedules,
			"count":     len(schedules),
		})
	case http.MethodPost:
		var sched Schedule
		if err := json.NewDecoder(r.Body).Decode(&sched); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if err := a.scheduler.CreateSchedule(r.Context(), &sched, user); err != nil {
			log.Printf("error: creating schedule: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to create schedule: "+err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, sched)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleScheduleByID(w http.ResponseWriter, r *http.Request) {
	// Parse: /api/v1/schedules/{id} or /api/v1/schedules/{id}/enable
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/schedules/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		respondError(w, http.StatusBadRequest, "schedule id required")
		return
	}

	user := r.Header.Get("X-Alcove-User")

	if len(parts) == 2 {
		switch parts[1] {
		case "enable":
			if r.Method != http.MethodPost {
				respondError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			var req struct {
				Enabled bool `json:"enabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
				return
			}
			if err := a.scheduler.EnableSchedule(r.Context(), id, req.Enabled, user); err != nil {
				log.Printf("error: enabling/disabling schedule %s: %v", id, err)
				respondError(w, http.StatusInternalServerError, "failed to update schedule: "+err.Error())
				return
			}
			respondJSON(w, http.StatusOK, map[string]bool{"updated": true})
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		sched, err := a.scheduler.GetSchedule(r.Context(), id, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "schedule not found")
			return
		}
		respondJSON(w, http.StatusOK, sched)
	case http.MethodPut:
		var sched Schedule
		if err := json.NewDecoder(r.Body).Decode(&sched); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		sched.ID = id
		if err := a.scheduler.UpdateSchedule(r.Context(), &sched, user); err != nil {
			log.Printf("error: updating schedule %s: %v", id, err)
			respondError(w, http.StatusInternalServerError, "failed to update schedule: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, sched)
	case http.MethodDelete:
		if err := a.scheduler.DeleteSchedule(r.Context(), id, user); err != nil {
			log.Printf("error: deleting schedule %s: %v", id, err)
			respondError(w, http.StatusInternalServerError, "failed to delete schedule: "+err.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Credentials ---

func (a *API) handleCredentials(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		creds, err := a.credStore.ListCredentials(r.Context(), user)
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
		cred := Credential{
			Name:      req.Name,
			Provider:  req.Provider,
			AuthType:  req.AuthType,
			ProjectID: req.ProjectID,
			Region:    req.Region,
			APIHost:   req.APIHost,
		}
		if err := a.credStore.CreateCredential(r.Context(), &cred, []byte(req.Credential), user); err != nil {
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

	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		cred, err := a.credStore.GetCredential(r.Context(), id, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "credential not found")
			return
		}
		respondJSON(w, http.StatusOK, cred)
	case http.MethodDelete:
		if err := a.credStore.DeleteCredential(r.Context(), id, user); err != nil {
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

// checkOwnership verifies the requesting user owns the session.
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
	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		tools, err := a.toolStore.ListTools(r.Context(), user)
		if err != nil {
			log.Printf("error: listing tools: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list tools")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"tools": tools,
			"count": len(tools),
		})
	case http.MethodPost:
		var tool ToolDefinition
		if err := json.NewDecoder(r.Body).Decode(&tool); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if tool.Name == "" || tool.DisplayName == "" {
			respondError(w, http.StatusBadRequest, "name and display_name are required")
			return
		}
		if err := a.toolStore.CreateTool(r.Context(), &tool, user); err != nil {
			log.Printf("error: creating tool: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to create tool: "+err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, tool)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleToolByID(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/tools/")
	if name == "" {
		respondError(w, http.StatusBadRequest, "tool name required")
		return
	}

	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		tool, err := a.toolStore.GetTool(r.Context(), name, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "tool not found")
			return
		}
		respondJSON(w, http.StatusOK, tool)
	case http.MethodPut:
		var tool ToolDefinition
		if err := json.NewDecoder(r.Body).Decode(&tool); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		tool.Name = name
		if err := a.toolStore.UpdateTool(r.Context(), &tool, user); err != nil {
			log.Printf("error: updating tool %s: %v", name, err)
			if strings.Contains(err.Error(), "builtin") {
				respondError(w, http.StatusForbidden, "builtin tools cannot be modified")
			} else {
				respondError(w, http.StatusNotFound, "tool not found or cannot be updated")
			}
			return
		}
		respondJSON(w, http.StatusOK, tool)
	case http.MethodDelete:
		// Check if it's a builtin tool first.
		existing, err := a.toolStore.GetTool(r.Context(), name, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "tool not found")
			return
		}
		if existing.ToolType == "builtin" {
			respondError(w, http.StatusForbidden, "builtin tools cannot be deleted")
			return
		}
		if err := a.toolStore.DeleteTool(r.Context(), name, user); err != nil {
			log.Printf("error: deleting tool %s: %v", name, err)
			respondError(w, http.StatusNotFound, "tool not found")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Security Profile Builder ---

func (a *API) handleSecurityProfileBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if a.llm == nil || !a.llm.Available() {
		respondError(w, http.StatusServiceUnavailable, "system LLM not configured")
		return
	}

	var req struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Description == "" {
		respondError(w, http.StatusBadRequest, "description is required")
		return
	}

	// Get available tools for the prompt context.
	user := r.Header.Get("X-Alcove-User")
	tools, _ := a.toolStore.ListTools(r.Context(), user)

	toolsDesc := ""
	for _, t := range tools {
		if t.Operations != nil {
			toolsDesc += fmt.Sprintf("Tool '%s' (%s) operations: %s\n", t.Name, t.DisplayName, string(t.Operations))
		}
	}

	systemPrompt := `You are a security profile builder for Alcove, a platform that runs AI coding agents.
Given a natural language description, generate a JSON security profile.

Available tools and their operations:
` + toolsDesc + `

IMPORTANT RULES:
1. "Code reading" or "read access" means: clone, read_contents, read_prs (GitHub) or read_mrs (GitLab), read_issues
2. "Opening a PR" means: clone, read_contents, push_branch, create_pr (or create_pr_draft for draft PRs)
3. "Opening an MR" means: clone, read_contents, push_branch, create_mr (or create_mr_draft for draft MRs)
4. "Commenting" means: create_comment
5. If a self-hosted GitLab URL is mentioned (not gitlab.com), note it in the description
6. Always include "clone" in the rule that has the repos the user wants to work with
7. GitHub repos use format "org/repo" (extract from URLs like https://github.com/org/repo/)
8. GitLab repos use format "group/project" (extract from URLs)
9. Use ["*"] for repos only when the user explicitly says "any repo" or "all repos"

RULES FORMAT:
Each tool config uses a "rules" array. Each rule specifies repos and operations that apply to those repos.
- When ALL operations apply to the SAME repos, use a single rule.
- When DIFFERENT operations apply to DIFFERENT repos, use multiple rules.
- Example: "read any repo but push only to pulp/pulpcore" produces two rules:
  Rule 1: repos=["*"], operations=["clone", "read_prs", "read_issues", "read_contents"]
  Rule 2: repos=["pulp/pulpcore"], operations=["clone", "push_branch", "create_pr_draft"]
- Global read operations (clone, read_prs, etc.) should use repos=["*"] when the user says "any repo" or "all repos".

Respond with ONLY a JSON object (no markdown, no explanation):
{
  "name": "short-kebab-case-name",
  "display_name": "Human Readable Name",
  "description": "Brief description including any self-hosted URLs",
  "tools": {
    "tool-name": {
      "rules": [
        { "repos": ["*"], "operations": ["clone", "read_prs"] },
        { "repos": ["org/repo"], "operations": ["clone", "push_branch", "create_pr_draft"] }
      ]
    }
  }
}`

	text, err := a.llm.Complete(r.Context(), systemPrompt, req.Description, 2048)
	if err != nil {
		log.Printf("error: profile build LLM call: %v", err)
		respondError(w, http.StatusInternalServerError, "LLM error: "+err.Error())
		return
	}

	// Strip any markdown code fences the LLM might have included.
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 2 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var profile SecurityProfile
	if err := json.Unmarshal([]byte(text), &profile); err != nil {
		log.Printf("error: profile build: failed to parse LLM response as profile JSON: %s", text)
		respondError(w, http.StatusInternalServerError, "failed to parse LLM response as profile")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"profile": profile,
	})
}

// --- Security Profiles ---

func (a *API) handleSecurityProfiles(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		profiles, err := a.profileStore.ListProfiles(r.Context(), user)
		if err != nil {
			log.Printf("error: listing profiles: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to list profiles")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"profiles": profiles,
			"count":    len(profiles),
		})
	case http.MethodPost:
		var profile SecurityProfile
		if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if profile.Name == "" {
			respondError(w, http.StatusBadRequest, "name is required")
			return
		}
		if profile.Tools == nil {
			respondError(w, http.StatusBadRequest, "tools is required")
			return
		}
		if err := a.profileStore.CreateProfile(r.Context(), &profile, user); err != nil {
			log.Printf("error: creating profile: %v", err)
			respondError(w, http.StatusInternalServerError, "failed to create profile: "+err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, profile)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleSecurityProfileByID(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/security-profiles/")
	if name == "" {
		respondError(w, http.StatusBadRequest, "profile name required")
		return
	}

	user := r.Header.Get("X-Alcove-User")

	switch r.Method {
	case http.MethodGet:
		profile, err := a.profileStore.GetProfile(r.Context(), name, user)
		if err != nil {
			respondError(w, http.StatusNotFound, "profile not found")
			return
		}
		respondJSON(w, http.StatusOK, profile)
	case http.MethodPut:
		var profile SecurityProfile
		if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		profile.Name = name
		if err := a.profileStore.UpdateProfile(r.Context(), &profile, user); err != nil {
			log.Printf("error: updating profile %s: %v", name, err)
			respondError(w, http.StatusNotFound, "profile not found or cannot be updated")
			return
		}
		respondJSON(w, http.StatusOK, profile)
	case http.MethodDelete:
		if err := a.profileStore.DeleteProfile(r.Context(), name, user); err != nil {
			log.Printf("error: deleting profile %s: %v", name, err)
			respondError(w, http.StatusNotFound, "profile not found")
			return
		}
		respondJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
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

func (a *API) handleAdminSettingsSkillRepos(w http.ResponseWriter, r *http.Request) {
	// Admin check.
	if r.Header.Get("X-Alcove-Admin") != "true" {
		respondError(w, http.StatusForbidden, "admin access required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		repos, err := a.settingsStore.GetSystemSkillRepos(r.Context())
		if err != nil {
			// No repos configured yet — return empty list.
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
		if err := a.settingsStore.SetSystemSkillRepos(r.Context(), req.Repos); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save skill repos")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"repos": req.Repos})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *API) handleUserSettingsSkillRepos(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		repos, err := a.settingsStore.GetUserSkillRepos(r.Context(), username)
		if err != nil {
			// No repos configured yet — return empty list.
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
		if err := a.settingsStore.SetUserSkillRepos(r.Context(), username, req.Repos); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save skill repos")
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"repos": req.Repos})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Task Repos Settings ---

func (a *API) handleUserSettingsTaskRepos(w http.ResponseWriter, r *http.Request) {
	username := r.Header.Get("X-Alcove-User")
	if username == "" {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	switch r.Method {
	case http.MethodGet:
		repos, err := a.settingsStore.GetUserTaskRepos(r.Context(), username)
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
		if err := a.settingsStore.SetUserTaskRepos(r.Context(), username, req.Repos); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to save task repos")
			return
		}
		go a.syncer.SyncAll(context.Background())
		respondJSON(w, http.StatusOK, map[string]any{"repos": req.Repos})
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// --- Task Repo Validation ---

func (a *API) handleTaskRepoValidate(w http.ResponseWriter, r *http.Request) {
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
	respondJSON(w, http.StatusOK, map[string]any{"valid": true, "task_count": len(names), "tasks": names})
}

// --- Task Definitions ---

func (a *API) handleTaskDefinitions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	defs, err := a.defStore.ListTaskDefinitions(r.Context(), user)
	if err != nil {
		log.Printf("error: listing task definitions: %v", err)
		respondError(w, http.StatusInternalServerError, "failed to list task definitions")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"task_definitions": defs,
		"count":            len(defs),
	})
}

func (a *API) handleTaskDefinitionsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if a.syncer == nil {
		respondError(w, http.StatusServiceUnavailable, "task repo syncer not configured")
		return
	}

	if err := a.syncer.SyncAll(r.Context()); err != nil {
		log.Printf("error: manual task sync: %v", err)
		respondError(w, http.StatusInternalServerError, "sync failed: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]bool{"synced": true})
}

func (a *API) handleTaskDefinitionByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/task-definitions/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if id == "" {
		respondError(w, http.StatusBadRequest, "task definition id required")
		return
	}

	// Handle /api/v1/task-definitions/{id}/run
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.handleTaskDefinitionRun(w, r, id)
		return
	}

	if r.Method != http.MethodGet {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	user := r.Header.Get("X-Alcove-User")
	def, err := a.defStore.GetTaskDefinition(r.Context(), id, user)
	if err != nil {
		respondError(w, http.StatusNotFound, "task definition not found")
		return
	}

	respondJSON(w, http.StatusOK, def)
}

func (a *API) handleTaskDefinitionRun(w http.ResponseWriter, r *http.Request, id string) {
	submitter := r.Header.Get("X-Alcove-User")
	if submitter == "" {
		submitter = "anonymous"
	}

	def, err := a.defStore.GetTaskDefinition(r.Context(), id, submitter)
	if err != nil {
		respondError(w, http.StatusNotFound, "task definition not found")
		return
	}

	if def.SyncError != "" {
		respondError(w, http.StatusBadRequest, "task definition has sync error: "+def.SyncError)
		return
	}

	req := def.ToTaskRequest()
	session, err := a.dispatcher.DispatchTask(r.Context(), req, submitter)
	if err != nil {
		log.Printf("error: dispatching task definition %s: %v", id, err)
		respondError(w, http.StatusInternalServerError, "failed to dispatch task: "+err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, session)
}

// --- Task Templates ---

func (a *API) handleTaskTemplates(w http.ResponseWriter, r *http.Request) {
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

	// Extract additional info for dispatched tasks.
	sha := ""
	prNumber := ""
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

	// Query schedules with event triggers.
	rows, queryErr := a.db.Query(ctx, `
		SELECT id, name, cron, prompt, repo, provider, scope_preset, timeout, enabled, owner, debug, trigger_type, event_config
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
			Owner       string
			Debug       bool
			TriggerType string
			EventConfig []byte
		}

		if err := rows.Scan(
			&sched.ID, &sched.Name, &sched.Cron, &sched.Prompt,
			&sched.Repo, &sched.Provider, &sched.ScopePreset, &sched.Timeout,
			&sched.Enabled, &sched.Owner, &sched.Debug, &sched.TriggerType, &sched.EventConfig,
		); err != nil {
			log.Printf("webhook: error scanning schedule: %v", err)
			continue
		}

		var trigger EventTrigger
		if err := json.Unmarshal(sched.EventConfig, &trigger); err != nil {
			log.Printf("webhook: error unmarshaling event_config for schedule %s: %v", sched.ID, err)
			continue
		}

		if trigger.GitHub == nil || !trigger.GitHub.Matches(eventType, action, repo, branch) {
			continue
		}

		matched++

		// Build TaskRequest with webhook context as env vars.
		taskReq := TaskRequest{
			Prompt:   sched.Prompt,
			Repo:     sched.Repo,
			Provider: sched.Provider,
			Timeout:  sched.Timeout,
			Debug:    sched.Debug,
		}

		session, err := a.dispatcher.DispatchTask(ctx, taskReq, sched.Owner)
		if err != nil {
			log.Printf("webhook: error dispatching schedule %s (%s): %v", sched.Name, sched.ID, err)
			continue
		}

		// Store webhook context as metadata on the session.
		webhookMeta := map[string]string{
			"GITHUB_EVENT":     eventType,
			"GITHUB_REPO":      repo,
			"GITHUB_REF":       branch,
			"GITHUB_SHA":       sha,
			"GITHUB_PR_NUMBER": prNumber,
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

// --- Helpers ---

func respondJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func respondError(w http.ResponseWriter, code int, msg string) {
	respondJSON(w, code, map[string]string{"error": msg})
}
