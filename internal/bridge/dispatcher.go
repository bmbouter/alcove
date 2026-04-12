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
	"strings"
	"sync"
	"time"

	"github.com/bmbouter/alcove/internal"
	"github.com/bmbouter/alcove/internal/runtime"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// Dispatcher manages task lifecycle: creation, dispatch to Skiff pods,
// and status tracking.
type Dispatcher struct {
	nc            *nats.Conn
	db            *pgxpool.Pool
	rt            runtime.Runtime
	cfg           *Config
	credStore     *CredentialStore
	toolStore     *ToolStore
	profileStore  *ProfileStore
	settingsStore *SettingsStore
	mu            sync.Mutex
	handles       map[string]runtime.TaskHandle // sessionID -> handle
	ciGate        *CIGateMonitor
}

// NewDispatcher creates a Dispatcher with the given dependencies.
func NewDispatcher(nc *nats.Conn, db *pgxpool.Pool, rt runtime.Runtime, cfg *Config, credStore *CredentialStore, toolStore *ToolStore, profileStore *ProfileStore, settingsStore *SettingsStore) *Dispatcher {
	return &Dispatcher{
		nc:            nc,
		db:            db,
		rt:            rt,
		cfg:           cfg,
		credStore:     credStore,
		toolStore:     toolStore,
		profileStore:  profileStore,
		settingsStore: settingsStore,
		handles:       make(map[string]runtime.TaskHandle),
	}
}

// SetCIGateMonitor attaches a CIGateMonitor to the dispatcher so that
// completed tasks with PR artifacts can be automatically monitored for CI.
func (d *Dispatcher) SetCIGateMonitor(m *CIGateMonitor) {
	d.ciGate = m
}

// TaskRequest is the JSON body for POST /api/v1/sessions.
type TaskRequest struct {
	Prompt   string                `json:"prompt"`
	Repo     string                `json:"repo,omitempty"`
	Provider string                `json:"provider,omitempty"`
	Timeout  int                   `json:"timeout,omitempty"` // seconds, default 3600
	Scope    *internal.Scope       `json:"scope,omitempty"`
	Tools    map[string]ToolConfig `json:"tools,omitempty"`
	Profiles []string              `json:"profiles,omitempty"`
	Model    string                `json:"model,omitempty"`
	Budget   float64               `json:"budget_usd,omitempty"`
	Debug    bool                  `json:"debug,omitempty"`
	Plugins  []PluginSpec `json:"-"` // Set internally from agent definition
	// Task metadata — set by dispatch code paths, stored in sessions table.
	TaskName    string `json:"-"` // Schedule/agent definition name
	TriggerType string `json:"-"` // "event", "cron", "manual", "webhook"
	TriggerRef  string `json:"-"` // e.g., "bmbouter/alcove#107" for GitHub events
}

// ToolConfig specifies per-tool configuration in a task request.
type ToolConfig struct {
	Enabled    bool     `json:"enabled"`
	Repos      []string `json:"repos,omitempty"`
	Operations []string `json:"operations,omitempty"`
}

// StatusUpdate is published by Skiff pods on NATS subject tasks.<id>.status.
type StatusUpdate struct {
	SessionID  string     `json:"session_id"`
	Status     string     `json:"status"`     // running, completed, timeout, cancelled, error
	ExitCode   *int       `json:"exit_code,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Artifacts  []internal.Artifact `json:"artifacts,omitempty"`
}

// DispatchTask creates a session record, publishes to Hail, and starts a
// Skiff pod via the Runtime.
func (d *Dispatcher) DispatchTask(ctx context.Context, req TaskRequest, submitter string) (*internal.Session, error) {
	sessionID := uuid.New().String()
	taskID := uuid.New().String()

	// Resolve provider: use explicit name or first available credential.
	provider := req.Provider
	if provider == "" {
		if firstCred, err := d.credStore.FirstAvailableProvider(ctx); err == nil {
			provider = firstCred.Name
		} else {
			// Fall back to env-based defaults.
			defaults := defaultProviders()
			if len(defaults) > 0 {
				provider = defaults[0].Name
			}
		}
	}

	// Default timeout: 1 hour.
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 3600
	}

	// Default scope: empty (no external access).
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	if req.Scope != nil {
		scope = *req.Scope
	}

	// Resolve security profiles into scope and tools.
	if len(req.Profiles) > 0 {
		profileScope, profileTools, err := d.profileStore.MergeProfiles(ctx, req.Profiles, submitter)
		if err != nil {
			return nil, fmt.Errorf("resolving profiles: %w", err)
		}

		// Merge profile scope into task scope.
		for svc, svcScope := range profileScope.Services {
			existing := scope.Services[svc]

			// Union operations.
			opSet := make(map[string]bool)
			for _, op := range existing.Operations {
				opSet[op] = true
			}
			for _, op := range svcScope.Operations {
				opSet[op] = true
			}
			ops := make([]string, 0, len(opSet))
			for op := range opSet {
				ops = append(ops, op)
			}

			// Union repos (wildcard wins).
			var repos []string
			if containsStr(existing.Repos, "*") || containsStr(svcScope.Repos, "*") {
				repos = []string{"*"}
			} else {
				repoSet := make(map[string]bool)
				for _, r := range existing.Repos {
					repoSet[r] = true
				}
				for _, r := range svcScope.Repos {
					repoSet[r] = true
				}
				repos = make([]string, 0, len(repoSet))
				for r := range repoSet {
					repos = append(repos, r)
				}
			}

			scope.Services[svc] = internal.ServiceScope{Operations: ops, Repos: repos}
		}

		// Populate req.Tools from profiles so the existing tool resolution runs.
		if req.Tools == nil {
			req.Tools = make(map[string]ToolConfig)
		}
		for tool, cfg := range profileTools {
			if _, exists := req.Tools[tool]; !exists {
				req.Tools[tool] = ToolConfig{
					Enabled:    true,
					Repos:      cfg.Repos,
					Operations: cfg.Operations,
				}
			}
		}
	}

	now := time.Now().UTC()
	session := &internal.Session{
		ID:          sessionID,
		TaskID:      taskID,
		Submitter:   submitter,
		Prompt:      req.Prompt,
		Repo:        req.Repo,
		Provider:    provider,
		Scope:       scope,
		Status:      "running",
		StartedAt:   now,
		TaskName:    req.TaskName,
		TriggerType: req.TriggerType,
		TriggerRef:  req.TriggerRef,
	}

	// Generate a session token for the Skiff pod to authenticate to Ledger.
	sessionToken := uuid.New().String()

	// Insert session record into Ledger.
	if err := d.insertSession(ctx, session, sessionToken); err != nil {
		return nil, fmt.Errorf("inserting session: %w", err)
	}

	// Publish task to Hail.
	task := internal.Task{
		ID:       taskID,
		Prompt:   req.Prompt,
		Repo:     req.Repo,
		Provider: provider,
		Scope:    scope,
		Timeout:  time.Duration(timeout) * time.Second,
		Budget:   req.Budget,
		Model:    req.Model,
	}

	taskBytes, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("marshaling task: %w", err)
	}

	if err := d.nc.Publish("tasks.dispatch", taskBytes); err != nil {
		return nil, fmt.Errorf("publishing to hail: %w", err)
	}

	// Resolve provider metadata from credential store.
	// Look up the credential by name to get provider type and model info.
	credMeta, _ := d.credStore.LookupProviderCredential(ctx, provider)

	model := req.Model
	if model == "" {
		// Try env-based defaults for known provider names.
		defaults := defaultProviders()
		for _, dp := range defaults {
			if dp.Name == provider || dp.Type == provider {
				model = dp.Model
				break
			}
		}
		if model == "" {
			model = "claude-sonnet-4-20250514"
		}
	}

	// Acquire LLM token from credential store.
	var llmToken, llmTokenType, llmProviderType string
	tokenResult, err := d.credStore.AcquireToken(ctx, provider)
	if err != nil && credMeta != nil && credMeta.Provider != provider {
		tokenResult, err = d.credStore.AcquireToken(ctx, credMeta.Provider)
	}
	if err != nil {
		log.Printf("warning: no credential found for provider %q: %v (falling back to env)", provider, err)
		llmToken = d.cfg.LLMKeyForProvider(provider)
		llmTokenType = "api_key"
		if credMeta != nil {
			llmProviderType = credMeta.Provider
		}
	} else {
		llmToken = tokenResult.Token
		llmTokenType = tokenResult.TokenType
		llmProviderType = tokenResult.Provider
	}

	// Build Skiff pod env vars.
	gateName := "gate-" + taskID
	skiffEnv := map[string]string{
		"TASK_ID":            taskID,
		"SESSION_ID":         sessionID,
		"SESSION_TOKEN":      sessionToken,
		"HAIL_URL":           envOrDefault("SKIFF_HAIL_URL", "nats://alcove-hail:4222"),
		"LEDGER_URL":         envOrDefault("BRIDGE_URL", fmt.Sprintf("http://alcove-bridge:%s", d.cfg.Port)),
		"PROMPT":             req.Prompt,
		"CLAUDE_MODEL":       model,
		"TASK_TIMEOUT":       fmt.Sprintf("%d", timeout),
		"ANTHROPIC_BASE_URL": fmt.Sprintf("http://%s:8443", gateName),
		"ANTHROPIC_API_KEY":  "sk-placeholder-routed-through-gate",
	}
	if req.Budget > 0 {
		skiffEnv["TASK_BUDGET"] = fmt.Sprintf("%.2f", req.Budget)
	}
	if req.Repo != "" {
		skiffEnv["REPO"] = req.Repo
	}

	// For Vertex AI, fetch project/region for Gate URL construction.
	var vertexRegion, vertexProject string
	if llmProviderType == "google-vertex" {
		vertexRegion = "us-east5"
		if err := d.db.QueryRow(ctx,
			`SELECT COALESCE(project_id, ''), COALESCE(region, 'us-east5') FROM provider_credentials WHERE provider = $1 OR name = $1 ORDER BY created_at DESC LIMIT 1`,
			provider).Scan(&vertexProject, &vertexRegion); err != nil {
			log.Printf("warning: failed to query Vertex AI project/region for provider %s: %v", provider, err)
		}
	}

	// Build Gate sidecar env vars (scope config + LLM credentials).
	scopeBytes, _ := json.Marshal(scope)
	gateEnv := map[string]string{
		"GATE_SESSION_ID":    sessionID,
		"GATE_SESSION_TOKEN": sessionToken,
		"GATE_SCOPE":         string(scopeBytes),
		"GATE_LLM_TOKEN":            llmToken,
		"GATE_LLM_PROVIDER":         llmProviderType,
		"GATE_LLM_TOKEN_TYPE":       llmTokenType,
		"GATE_TOKEN_REFRESH_URL":    envOrDefault("BRIDGE_URL", fmt.Sprintf("http://alcove-bridge:%s", d.cfg.Port)) + "/api/v1/internal/token-refresh",
		"GATE_TOKEN_REFRESH_SECRET": sessionToken,
		"GATE_LEDGER_URL":    envOrDefault("BRIDGE_URL", fmt.Sprintf("http://alcove-bridge:%s", d.cfg.Port)),
		"GATE_CREDENTIALS":   "{}",
		"GATE_VERTEX_REGION":  vertexRegion,
		"GATE_VERTEX_PROJECT": vertexProject,
	}

	// Resolve SCM credentials for services in scope.
	scmCredentials := make(map[string]string)
	scmDummyTokens := make(map[string]string)
	scmAPIHosts := make(map[string]string) // service -> custom api_host from credential
	for service := range scope.Services {
		if service == "github" || service == "gitlab" || service == "jira" {
			realToken, apiHost, err := d.credStore.AcquireSCMTokenWithHost(ctx, service)
			if err != nil {
				log.Printf("warning: no credential for %s: %v", service, err)
				continue
			}
			scmCredentials[service] = realToken
			dummyToken := "alcove-session-" + uuid.New().String()
			scmDummyTokens[service] = dummyToken
			if apiHost != "" {
				scmAPIHosts[service] = stripURLToHost(apiHost)
			}
		}
	}

	// Replace the empty GATE_CREDENTIALS with actual service credentials.
	if len(scmCredentials) > 0 {
		credJSON, _ := json.Marshal(scmCredentials)
		gateEnv["GATE_CREDENTIALS"] = string(credJSON)
	}

	// Resolve MCP tool configurations for the task.
	var mcpConfigs map[string]any
	var gateToolConfigs map[string]any

	if len(req.Tools) > 0 {
		mcpConfigs = make(map[string]any)
		gateToolConfigs = make(map[string]any)

		for toolName, toolCfg := range req.Tools {
			if !toolCfg.Enabled {
				continue
			}

			// Look up tool definition.
			tool, err := d.toolStore.GetTool(ctx, toolName, submitter)
			if err != nil {
				log.Printf("warning: tool %q not found: %v", toolName, err)
				continue
			}

			// Add to scope.
			scope.Services[toolName] = internal.ServiceScope{
				Repos:      toolCfg.Repos,
				Operations: toolCfg.Operations,
			}

			// Resolve credential for this tool.
			realToken, apiHost, err := d.credStore.AcquireSCMTokenWithHost(ctx, toolName)
			dummyToken := "alcove-session-" + uuid.New().String()

			if err != nil {
				log.Printf("warning: no credential for tool %q: %v", toolName, err)
				realToken = ""
			}

			// Add real credential to Gate config.
			if realToken != "" {
				scmCredentials[toolName] = realToken
			}

			// If credential has a custom API host, track it for overriding tool config.
			if apiHost != "" {
				scmAPIHosts[toolName] = stripURLToHost(apiHost)
			}

			// Build MCP server config for Skiff.
			if tool.MCPCommand != "" {
				envMap := map[string]string{}

				// Set the tool's API URL to go through Gate.
				switch toolName {
				case "github":
					envMap["GITHUB_PERSONAL_ACCESS_TOKEN"] = dummyToken
					envMap["GITHUB_HOST"] = fmt.Sprintf("http://%s:8443/github", gateName)
				case "gitlab":
					envMap["GITLAB_TOKEN"] = dummyToken
					envMap["GITLAB_API_URL"] = fmt.Sprintf("http://%s:8443/gitlab/api/v4", gateName)
				default:
					// Custom tools: set a generic token env var.
					envMap["TOOL_TOKEN"] = dummyToken
					if tool.APIHost != "" {
						envMap["API_BASE_URL"] = fmt.Sprintf("http://%s:8443/%s", gateName, toolName)
					}
				}

				// Parse MCP args.
				var args []string
				if tool.MCPArgs != nil {
					if err := json.Unmarshal(tool.MCPArgs, &args); err != nil {
						log.Printf("warning: failed to unmarshal MCP args for tool %s: %v", toolName, err)
					}
				}

				mcpConfigs[toolName] = map[string]any{
					"command": tool.MCPCommand,
					"args":    args,
					"env":     envMap,
				}
			}

			// Gate tool config.
			if tool.APIHost != "" {
				gateToolConfigs[toolName] = map[string]string{
					"api_host":    tool.APIHost,
					"auth_header": tool.AuthHeader,
					"auth_format": tool.AuthFormat,
				}
			}
		}

		// Set Skiff MCP config env var.
		if len(mcpConfigs) > 0 {
			mcpJSON, _ := json.Marshal(mcpConfigs)
			skiffEnv["ALCOVE_MCP_CONFIG"] = string(mcpJSON)
		}

		// Set Gate tool configs env var.
		if len(gateToolConfigs) > 0 {
			gtcJSON, _ := json.Marshal(gateToolConfigs)
			gateEnv["GATE_TOOL_CONFIGS"] = string(gtcJSON)
		}
	}

	// Re-marshal credentials after tool resolution may have added more.
	if len(scmCredentials) > 0 {
		credJSON, _ := json.Marshal(scmCredentials)
		gateEnv["GATE_CREDENTIALS"] = string(credJSON)
	}

	// Override Gate tool configs with custom API hosts from credentials.
	if len(scmAPIHosts) > 0 {
		if gateToolConfigs == nil {
			gateToolConfigs = make(map[string]any)
		}
		for service, apiHost := range scmAPIHosts {
			if existing, ok := gateToolConfigs[service]; ok {
				// Override the api_host in the existing tool config.
				if m, ok := existing.(map[string]string); ok {
					m["api_host"] = apiHost
					gateToolConfigs[service] = m
				}
			} else {
				// Create a new tool config entry with the credential's api_host.
				switch service {
				case "gitlab":
					gateToolConfigs[service] = map[string]string{
						"api_host":    apiHost,
						"auth_header": "PRIVATE-TOKEN",
						"auth_format": "header",
					}
				case "github":
					gateToolConfigs[service] = map[string]string{
						"api_host":    apiHost,
						"auth_header": "Authorization",
						"auth_format": "bearer",
					}
				case "jira":
					gateToolConfigs[service] = map[string]string{
						"api_host":    apiHost,
						"auth_header": "Authorization",
						"auth_format": "basic",
					}
				}
			}
		}

		// Set Gate tool configs env var if we added entries.
		if len(gateToolConfigs) > 0 {
			gtcJSON, _ := json.Marshal(gateToolConfigs)
			gateEnv["GATE_TOOL_CONFIGS"] = string(gtcJSON)
		}

		// Set GATE_GITLAB_HOST for custom GitLab instances.
		if gitlabHost, ok := scmAPIHosts["gitlab"]; ok {
			gateEnv["GATE_GITLAB_HOST"] = gitlabHost
		}
	}

	// Re-marshal scope after tool resolution may have added services.
	scopeBytes, _ = json.Marshal(scope)
	gateEnv["GATE_SCOPE"] = string(scopeBytes)

	// Set SCM environment for Skiff tools (dummy tokens + Gate proxy URLs).
	if token, ok := scmDummyTokens["github"]; ok {
		skiffEnv["GITHUB_TOKEN"] = token
		skiffEnv["GH_TOKEN"] = token
		skiffEnv["GITHUB_PERSONAL_ACCESS_TOKEN"] = token
		skiffEnv["GITHUB_API_URL"] = fmt.Sprintf("http://%s:8443/github", gateName)
		skiffEnv["GH_HOST"] = fmt.Sprintf("%s:8443", gateName)
		skiffEnv["GH_PROTOCOL"] = "http"
		skiffEnv["GH_PROMPT_DISABLED"] = "1"
		skiffEnv["GH_NO_UPDATE_NOTIFIER"] = "1"
	}
	if token, ok := scmDummyTokens["gitlab"]; ok {
		skiffEnv["GITLAB_TOKEN"] = token
		skiffEnv["GITLAB_PERSONAL_ACCESS_TOKEN"] = token
		skiffEnv["GITLAB_API_URL"] = fmt.Sprintf("http://%s:8443/gitlab/api/v4", gateName)
		skiffEnv["GLAB_HOST"] = fmt.Sprintf("http://%s:8443/gitlab", gateName)
	}
	if token, ok := scmDummyTokens["jira"]; ok {
		skiffEnv["JIRA_TOKEN"] = token
		skiffEnv["JIRA_API_URL"] = fmt.Sprintf("http://%s:8443/jira", gateName)
	}

	// Resolve skill repos for this task.
	var skillRepos []SkillRepo

	// System-wide repos.
	if systemRepos, err := d.settingsStore.GetSystemSkillRepos(ctx); err == nil {
		skillRepos = append(skillRepos, systemRepos...)
	}

	// User-specific repos.
	if userRepos, err := d.settingsStore.GetUserSkillRepos(ctx, submitter); err == nil {
		skillRepos = append(skillRepos, userRepos...)
	}

	if len(skillRepos) > 0 {
		reposJSON, _ := json.Marshal(skillRepos)
		skiffEnv["ALCOVE_SKILL_REPOS"] = string(reposJSON)
	}

	// Resolve plugins from agent definition.
	// Plugins are specified in the task definition and passed to Skiff for installation.
	// Resolve plugin bundles before passing to Skiff.
	plugins := ResolvePluginBundles(req.Plugins)
	if len(plugins) > 0 {
		pluginsJSON, _ := json.Marshal(plugins)
		skiffEnv["ALCOVE_PLUGINS"] = string(pluginsJSON)
	}

	// Start Skiff pod via Runtime.
	spec := runtime.TaskSpec{
		TaskID:      taskID,
		Image:       envOrDefault("SKIFF_IMAGE", "localhost/alcove-skiff-base:dev"),
		GateImage:   envOrDefault("GATE_IMAGE", "localhost/alcove-gate:dev"),
		Env:         skiffEnv,
		GateEnv:     gateEnv,
		Timeout:     int64(timeout),
		Network:     envOrDefault("ALCOVE_NETWORK", runtime.DefaultInternalNetwork),
		ExternalNet: envOrDefault("ALCOVE_EXTERNAL_NETWORK", runtime.DefaultExternalNetwork),
		Debug:       req.Debug || d.cfg.DebugMode,
	}

	handle, err := d.rt.RunTask(ctx, spec)
	if err != nil {
		// Update session to error state.
		d.updateSessionStatus(ctx, sessionID, "error", nil, nil)
		return nil, fmt.Errorf("starting skiff pod: %w", err)
	}

	d.mu.Lock()
	d.handles[sessionID] = handle
	d.mu.Unlock()

	return session, nil
}

// CancelSession cancels a running session.
func (d *Dispatcher) CancelSession(ctx context.Context, sessionID string) error {
	d.mu.Lock()
	handle, ok := d.handles[sessionID]
	d.mu.Unlock()

	if !ok {
		// Session not tracked in memory (e.g., Bridge restarted while task was
		// running). Update DB status to cancelled — the container is already gone.
		now := time.Now().UTC()
		d.updateSessionStatus(ctx, sessionID, "cancelled", nil, &now)
		return nil
	}

	// Send cancel via NATS.
	if err := d.nc.Publish(fmt.Sprintf("tasks.%s.cancel", sessionID), nil); err != nil {
		log.Printf("warning: failed to publish cancel to hail: %v", err)
	}

	// Cancel via Runtime.
	if err := d.rt.CancelTask(ctx, handle); err != nil {
		return fmt.Errorf("canceling task: %w", err)
	}

	now := time.Now().UTC()
	d.updateSessionStatus(ctx, sessionID, "cancelled", nil, &now)
	d.mu.Lock()
	delete(d.handles, sessionID)
	d.mu.Unlock()

	return nil
}

// ListenForStatusUpdates subscribes to NATS status updates from Skiff pods.
func (d *Dispatcher) ListenForStatusUpdates(ctx context.Context) error {
	_, err := d.nc.Subscribe("tasks.*.status", func(msg *nats.Msg) {
		var update StatusUpdate
		if err := json.Unmarshal(msg.Data, &update); err != nil {
			log.Printf("error: malformed status update: %v", err)
			return
		}

		log.Printf("status update: session=%s status=%s", update.SessionID, update.Status)
		d.updateSessionStatus(ctx, update.SessionID, update.Status, update.ExitCode, update.FinishedAt)

		// Update artifacts if provided.
		if len(update.Artifacts) > 0 {
			d.updateSessionArtifacts(ctx, update.SessionID, update.Artifacts)
		}

		// Clean up handle on terminal states.
		if update.Status == "completed" || update.Status == "error" || update.Status == "timeout" {
			d.mu.Lock()
			delete(d.handles, update.SessionID)
			d.mu.Unlock()

			// Notify CI Gate monitor if task completed with artifacts.
			if d.ciGate != nil && update.Status == "completed" {
				go d.ciGate.OnTaskCompleted(ctx, update.SessionID, update.Artifacts)
			}
		}
	})

	return err
}

// insertSession writes a new session record to the Ledger (PostgreSQL).
func (d *Dispatcher) insertSession(ctx context.Context, s *internal.Session, sessionToken string) error {
	scopeJSON, _ := json.Marshal(s.Scope)
	_, err := d.db.Exec(ctx, `
		INSERT INTO sessions (id, task_id, submitter, prompt, scope, provider, started_at, outcome, session_token, task_name, trigger_type, trigger_ref, repo)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, s.ID, s.TaskID, s.Submitter, s.Prompt, scopeJSON, s.Provider, s.StartedAt, s.Status, sessionToken,
		nilIfEmpty(s.TaskName), nilIfEmpty(s.TriggerType), nilIfEmpty(s.TriggerRef), nilIfEmpty(s.Repo))
	return err
}

func (d *Dispatcher) updateSessionStatus(ctx context.Context, sessionID, status string, exitCode *int, finishedAt *time.Time) {
	_, err := d.db.Exec(ctx, `
		UPDATE sessions SET outcome = $1, exit_code = $2, finished_at = $3
		WHERE id = $4
	`, status, exitCode, finishedAt, sessionID)
	if err != nil {
		log.Printf("error: updating session %s status: %v", sessionID, err)
	}
}

// stripURLToHost extracts the hostname from a value that may be a full URL
// (e.g., "https://redhat.atlassian.net/") or just a hostname. Returns just
// the host without scheme, port path, or trailing slash.
func stripURLToHost(apiHost string) string {
	h := apiHost
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimRight(h, "/")
	// Strip port if present but keep hostname.
	if idx := strings.Index(h, "/"); idx != -1 {
		h = h[:idx]
	}
	return h
}

// RecoverHandles rebuilds the in-memory handles map from sessions still
// marked as running in the database. This handles Bridge restarts where
// the map is lost. Sessions whose containers have already exited are
// marked as completed.
func (d *Dispatcher) RecoverHandles(ctx context.Context) {
	if d.db == nil {
		return
	}
	rows, err := d.db.Query(ctx,
		`SELECT id, task_id FROM sessions WHERE outcome = 'running'`)
	if err != nil {
		log.Printf("reconcile: error querying running sessions: %v", err)
		return
	}
	defer rows.Close()

	var recovered, orphaned int
	for rows.Next() {
		var sessionID, taskID string
		if err := rows.Scan(&sessionID, &taskID); err != nil {
			continue
		}

		// Check if the container/job still exists via Runtime.
		handle := runtime.TaskHandle{ID: taskID}
		status, err := d.rt.TaskStatus(ctx, handle)
		if err != nil || status == "not_found" {
			// Container is gone — mark session as completed.
			now := time.Now().UTC()
			d.updateSessionStatus(ctx, sessionID, "completed", nil, &now)
			orphaned++
			log.Printf("reconcile: marked orphaned session %s as completed (container gone)", sessionID)
			continue
		}

		if status == "exited" || status == "stopped" {
			now := time.Now().UTC()
			d.updateSessionStatus(ctx, sessionID, "completed", nil, &now)
			orphaned++
			log.Printf("reconcile: marked exited session %s as completed", sessionID)
			continue
		}

		// Container still running — add to handles map.
		d.mu.Lock()
		d.handles[sessionID] = handle
		d.mu.Unlock()
		recovered++
	}

	if recovered > 0 || orphaned > 0 {
		log.Printf("reconcile: recovered %d running session(s), cleaned up %d orphaned session(s)", recovered, orphaned)
	}
}

// ReconcileLoop periodically checks for sessions stuck in "running" state
// whose containers have exited. This catches status updates lost during
// Bridge restarts or NATS message drops.
func (d *Dispatcher) ReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.RecoverHandles(ctx)
		}
	}
}

func (d *Dispatcher) updateSessionArtifacts(ctx context.Context, sessionID string, artifacts []internal.Artifact) {
	artifactsJSON, _ := json.Marshal(artifacts)
	_, err := d.db.Exec(ctx, `
		UPDATE sessions SET artifacts = $1 WHERE id = $2
	`, artifactsJSON, sessionID)
	if err != nil {
		log.Printf("error: updating session %s artifacts: %v", sessionID, err)
	}
}
