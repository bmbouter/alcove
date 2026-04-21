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
	"crypto/rand"
	"encoding/hex"
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
	nc             *nats.Conn
	db             *pgxpool.Pool
	rt             runtime.Runtime
	cfg            *Config
	credStore      *CredentialStore
	toolStore      *ToolStore
	profileStore   *ProfileStore
	settingsStore  *SettingsStore
	mu             sync.Mutex
	handles        map[string]runtime.TaskHandle // sessionID -> handle
	ciGate         *CIGateMonitor
	workflowEngine *WorkflowEngine
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

// SetWorkflowEngine attaches a WorkflowEngine to the dispatcher so that
// completed sessions can trigger workflow step completion handling.
func (d *Dispatcher) SetWorkflowEngine(we *WorkflowEngine) {
	d.workflowEngine = we
}

// generateShimToken returns a cryptographically random hex-encoded token
// of the specified byte length (the resulting string is 2*n hex characters).
func generateShimToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// TaskRequest is the JSON body for POST /api/v1/sessions.
type TaskRequest struct {
	Prompt         string                   `json:"prompt,omitempty"`
	Executable     *internal.ExecutableSpec `json:"executable,omitempty"`
	Repos          []internal.RepoSpec      `json:"repos,omitempty"`
	Provider       string                   `json:"provider,omitempty"`
	Timeout        int                      `json:"timeout,omitempty"` // seconds, default 3600
	Scope          *internal.Scope          `json:"scope,omitempty"`
	Tools          map[string]ToolConfig    `json:"tools,omitempty"`
	Profiles       []string                 `json:"profiles,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Budget         float64                  `json:"budget_usd,omitempty"`
	Debug          bool                     `json:"debug,omitempty"`
	Plugins        []PluginSpec             `json:"-"` // Set internally from agent definition
	Credentials    map[string]string        `json:"-"` // ENV_VAR_NAME: credential_provider_name
	DirectOutbound bool                     `json:"direct_outbound,omitempty"`
	DevContainer   *DevContainerSpec       `json:"dev_container,omitempty"`
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
	SessionID  string              `json:"session_id"`
	Status     string              `json:"status"` // running, completed, timeout, cancelled, error
	ExitCode   *int                `json:"exit_code,omitempty"`
	FinishedAt *time.Time          `json:"finished_at,omitempty"`
	Artifacts  []internal.Artifact `json:"artifacts,omitempty"`
	Outputs    map[string]string   `json:"outputs,omitempty"` // Agent-produced outputs from /tmp/alcove-outputs.json
}

// DispatchTask creates a session record, publishes to Hail, and starts a
// Skiff pod via the Runtime. The optional teamID parameter associates the
// session with a team; if empty, the session has no team association.
func (d *Dispatcher) DispatchTask(ctx context.Context, req TaskRequest, submitter string, teamID ...string) (*internal.Session, error) {
	sessionID := uuid.New().String()
	taskID := uuid.New().String()

	// Resolve provider: use explicit name or first available credential.
	// "workflow" is a placeholder used by workflow schedule entries, not a real provider.
	provider := req.Provider
	if provider == "" || provider == "workflow" {
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

	// Extract team ID from variadic parameter (needed early for profile resolution).
	var activeTeamID string
	if len(teamID) > 0 {
		activeTeamID = teamID[0]
	}

	// Default scope: empty (no external access).
	scope := internal.Scope{Services: map[string]internal.ServiceScope{}}
	if req.Scope != nil {
		scope = *req.Scope
	}

	// Resolve security profiles into scope and tools.
	if len(req.Profiles) > 0 {
		profileScope, profileTools, err := d.profileStore.MergeProfiles(ctx, req.Profiles, activeTeamID)
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
		Repos:       req.Repos,
		Provider:    provider,
		Scope:       scope,
		Status:      "running",
		StartedAt:   now,
		TaskName:    req.TaskName,
		TriggerType: req.TriggerType,
		TriggerRef:  req.TriggerRef,
		TeamID:      activeTeamID,
	}

	// For executable agents, set a descriptive prompt if empty.
	if req.Executable != nil && req.Prompt == "" {
		req.Prompt = fmt.Sprintf("Executable agent: %s", req.Executable.URL)
		session.Prompt = req.Prompt
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
		Repos:    req.Repos,
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
	}

	// For claude-oauth (Pro/Max), pass the real OAuth token as
	// ANTHROPIC_AUTH_TOKEN which Claude Code sends as Authorization: Bearer.
	// This works in --bare mode (unlike CLAUDE_CODE_OAUTH_TOKEN) and the
	// Anthropic API accepts OAuth tokens via Bearer auth.
	// For all other providers, use a placeholder API key that Gate replaces.
	if llmTokenType == "oauth_token" {
		skiffEnv["ANTHROPIC_AUTH_TOKEN"] = llmToken
	} else {
		skiffEnv["ANTHROPIC_API_KEY"] = "sk-placeholder-routed-through-gate"
	}

	// Pass executable configuration to Skiff via environment variable
	if req.Executable != nil {
		execJSON, _ := json.Marshal(req.Executable)
		skiffEnv["ALCOVE_EXECUTABLE"] = string(execJSON)
	}
	if req.Budget > 0 {
		skiffEnv["TASK_BUDGET"] = fmt.Sprintf("%.2f", req.Budget)
	}
	if len(req.Repos) > 0 {
		reposJSON, _ := json.Marshal(req.Repos)
		skiffEnv["REPOS"] = string(reposJSON)
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
		"GATE_SESSION_ID":           sessionID,
		"GATE_SESSION_TOKEN":        sessionToken,
		"GATE_SCOPE":                string(scopeBytes),
		"GATE_LLM_TOKEN":            llmToken,
		"GATE_LLM_PROVIDER":         llmProviderType,
		"GATE_LLM_TOKEN_TYPE":       llmTokenType,
		"GATE_TOKEN_REFRESH_URL":    envOrDefault("BRIDGE_URL", fmt.Sprintf("http://alcove-bridge:%s", d.cfg.Port)) + "/api/v1/internal/token-refresh",
		"GATE_TOKEN_REFRESH_SECRET": sessionToken,
		"GATE_LEDGER_URL":           envOrDefault("BRIDGE_URL", fmt.Sprintf("http://alcove-bridge:%s", d.cfg.Port)),
		"GATE_CREDENTIALS":          "{}",
		"GATE_VERTEX_REGION":        vertexRegion,
		"GATE_VERTEX_PROJECT":       vertexProject,
	}

	// Resolve SCM credentials for services in scope.
	scmCredentials := make(map[string]string)
	scmDummyTokens := make(map[string]string)
	scmAPIHosts := make(map[string]string) // service -> custom api_host from credential
	for service := range scope.Services {
		if service == "github" || service == "gitlab" || service == "jira" || service == "splunk" {
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
			tool, err := d.toolStore.GetTool(ctx, toolName, activeTeamID)
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
				case "splunk":
					gateToolConfigs[service] = map[string]string{
						"api_host":    apiHost,
						"auth_header": "Authorization",
						"auth_format": "bearer",
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
	if token, ok := scmDummyTokens["splunk"]; ok {
		skiffEnv["SPLUNK_TOKEN"] = token
		skiffEnv["SPLUNK_URL"] = fmt.Sprintf("http://%s:8443/splunk", gateName)
	}

	// Resolve skill repos for this task (catalog-based for team sessions).
	var skillRepos []SkillRepo

	if activeTeamID != "" {
		// Team-based: resolve from catalog + custom plugins.
		catalog := LoadCatalog()
		var enabledMapJSON json.RawMessage
		if err := d.db.QueryRow(ctx,
			`SELECT value FROM team_settings WHERE team_id = $1 AND key = 'catalog'`,
			activeTeamID).Scan(&enabledMapJSON); err == nil {
			var enabledMap map[string]bool
			if json.Unmarshal(enabledMapJSON, &enabledMap) == nil {
				skillRepos = append(skillRepos, ResolveCatalogSkillRepos(catalog, enabledMap)...)
			}
		}
		var customJSON json.RawMessage
		if err := d.db.QueryRow(ctx,
			`SELECT value FROM team_settings WHERE team_id = $1 AND key = 'custom_plugins'`,
			activeTeamID).Scan(&customJSON); err == nil {
			var customPlugins []SkillRepo
			if json.Unmarshal(customJSON, &customPlugins) == nil {
				skillRepos = append(skillRepos, customPlugins...)
			}
		}
	} else {
		// Non-team sessions: use legacy system + user repos.
		if systemRepos, err := d.settingsStore.GetSystemSkillRepos(ctx); err == nil {
			skillRepos = append(skillRepos, systemRepos...)
		}
		if userRepos, err := d.settingsStore.GetUserSkillRepos(ctx, submitter); err == nil {
			skillRepos = append(skillRepos, userRepos...)
		}
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

	// Resolve credentials from agent definition.
	for envVar, credName := range req.Credentials {
		// Try AcquireToken first (works for LLM and generic secrets).
		tokenResult, err := d.credStore.AcquireToken(ctx, credName)
		if err == nil {
			skiffEnv[envVar] = tokenResult.Token
			continue
		}
		// Fall back to SCM token path.
		token, _, err := d.credStore.AcquireSCMTokenWithHost(ctx, credName)
		if err != nil {
			log.Printf("warning: credential %q not found for env var %s", credName, envVar)
			continue
		}
		skiffEnv[envVar] = token
	}

	// Build runtime config for session visibility.
	runtimeConfig := map[string]any{
		"model":           model,
		"direct_outbound": req.DirectOutbound,
		"timeout":         timeout,
	}
	if len(req.Profiles) > 0 {
		runtimeConfig["profiles"] = req.Profiles
	}
	if len(scope.Services) > 0 {
		runtimeConfig["scope"] = scope
	}
	if len(plugins) > 0 {
		runtimeConfig["plugins"] = plugins
	}
	if len(skillRepos) > 0 {
		runtimeConfig["skill_repos"] = skillRepos
	}

	// Credential mapping (env var -> provider + classification, NO values).
	var credEntries []map[string]string
	for envVar, credName := range req.Credentials {
		cls := "real"
		if v, ok := skiffEnv[envVar]; ok && (strings.HasPrefix(v, "alcove-session-") || v == "sk-placeholder-routed-through-gate") {
			cls = "dummy"
		}
		credEntries = append(credEntries, map[string]string{
			"env_var": envVar, "provider": credName, "classification": cls,
		})
	}
	// Add SCM credentials from scope.
	for service := range scmDummyTokens {
		credEntries = append(credEntries, map[string]string{
			"env_var": strings.ToUpper(service) + "_TOKEN", "provider": service, "classification": "dummy",
		})
	}
	if len(credEntries) > 0 {
		runtimeConfig["credentials"] = credEntries
	}

	if req.DevContainer != nil && req.DevContainer.Image != "" {
		runtimeConfig["dev_container"] = req.DevContainer
	}

	runtimeConfigJSON, _ := json.Marshal(runtimeConfig)
	_, _ = d.db.Exec(ctx, `UPDATE sessions SET runtime_config = $1 WHERE id = $2`, runtimeConfigJSON, sessionID)

	// Start Skiff pod via Runtime.
	spec := runtime.TaskSpec{
		TaskID:         taskID,
		Image:          envOrDefault("SKIFF_IMAGE", "ghcr.io/bmbouter/alcove-skiff-base:latest"),
		GateImage:      envOrDefault("GATE_IMAGE", "ghcr.io/bmbouter/alcove-gate:latest"),
		Env:            skiffEnv,
		GateEnv:        gateEnv,
		Timeout:        int64(timeout),
		Network:        envOrDefault("ALCOVE_NETWORK", runtime.DefaultInternalNetwork),
		ExternalNet:    envOrDefault("ALCOVE_EXTERNAL_NETWORK", runtime.DefaultExternalNetwork),
		Debug:          req.Debug || d.cfg.DebugMode,
		DirectOutbound: req.DirectOutbound,
	}

	if req.DevContainer != nil && req.DevContainer.Image != "" {
		spec.DevContainerImage = req.DevContainer.Image
		spec.DevContainerNetworkAccess = req.DevContainer.NetworkAccess

		shimToken := generateShimToken(32)
		spec.DevContainerEnv = map[string]string{
			"SHIM_TOKEN": shimToken,
		}

		devHost := runtime.DevContainerName(taskID) + ":9090"
		skiffEnv["DEV_TOKEN"] = shimToken
		skiffEnv["DEV_CONTAINER_HOST"] = devHost
	}

	handle, err := d.rt.RunTask(ctx, spec)
	if err != nil {
		// Update session to error state and store the startup error detail.
		d.updateSessionStatus(ctx, sessionID, "error", nil, nil)
		runtimeConfig["startup_error"] = err.Error()
		runtimeConfigJSON, _ = json.Marshal(runtimeConfig)
		d.db.Exec(ctx, `UPDATE sessions SET runtime_config = $1 WHERE id = $2`, runtimeConfigJSON, sessionID)
		if d.workflowEngine != nil {
			if wfErr := d.workflowEngine.OnStepCompletion(ctx, sessionID, "error", nil); wfErr != nil {
				log.Printf("error handling workflow step failure for session %s: %v", sessionID, wfErr)
			}
		}
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
		// Notify workflow engine so workflow steps don't stay stuck in "running".
		if d.workflowEngine != nil {
			if err := d.workflowEngine.OnStepCompletion(ctx, sessionID, "cancelled", nil); err != nil {
				log.Printf("error handling workflow step cancellation for session %s: %v", sessionID, err)
			}
		}
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

	// Notify workflow engine so workflow steps don't stay stuck in "running".
	if d.workflowEngine != nil {
		if err := d.workflowEngine.OnStepCompletion(ctx, sessionID, "cancelled", nil); err != nil {
			log.Printf("error handling workflow step cancellation for session %s: %v", sessionID, err)
		}
	}

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

		// Update outputs if provided and this session is part of a workflow.
		if len(update.Outputs) > 0 {
			d.updateWorkflowStepOutputs(ctx, update.SessionID, update.Outputs)
		}

		// Clean up handle on terminal states.
		if update.Status == "completed" || update.Status == "error" || update.Status == "timeout" {
			d.mu.Lock()
			handle, hasHandle := d.handles[update.SessionID]
			delete(d.handles, update.SessionID)
			d.mu.Unlock()

			// Clean up the Gate sidecar container after a grace period.
			// The goroutine waits 5 seconds so Gate can flush final logs.
			// Uses a detached context so cleanup completes even if the
			// parent context is cancelled (e.g., during Bridge shutdown).
			if hasHandle {
				go func(taskID, sessionID string) {
					time.Sleep(5 * time.Second)
					gateName := runtime.GateContainerName(taskID)
					log.Printf("cleanup: stopping gate sidecar %s for completed session %s", gateName, sessionID)
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := d.rt.StopService(cleanupCtx, gateName); err != nil {
						log.Printf("cleanup: failed to stop gate %s: %v", gateName, err)
					}
					// Clean up dev container and workspace volume (no-op if not present).
					devName := runtime.DevContainerName(taskID)
					if err := d.rt.StopService(cleanupCtx, devName); err != nil {
						log.Printf("cleanup: failed to stop dev container %s: %v (may not exist)", devName, err)
					}
				}(handle.ID, update.SessionID)
			}

			// Notify workflow engine of step completion
			if d.workflowEngine != nil {
				if err := d.workflowEngine.OnStepCompletion(ctx, update.SessionID, update.Status, update.ExitCode); err != nil {
					log.Printf("error handling workflow step completion for session %s: %v", update.SessionID, err)
				}
			}

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
	var reposJSON []byte
	if len(s.Repos) > 0 {
		reposJSON, _ = json.Marshal(s.Repos)
	}
	_, err := d.db.Exec(ctx, `
		INSERT INTO sessions (id, task_id, submitter, prompt, scope, provider, started_at, outcome, session_token, task_name, trigger_type, trigger_ref, repos, team_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, s.ID, s.TaskID, s.Submitter, s.Prompt, scopeJSON, s.Provider, s.StartedAt, s.Status, sessionToken,
		nilIfEmpty(s.TaskName), nilIfEmpty(s.TriggerType), nilIfEmpty(s.TriggerRef), reposJSON, s.TeamID)
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

		// Check for container startup errors (e.g., ImagePullBackOff, CrashLoopBackOff).
		if err == nil && strings.HasPrefix(status, "error:") {
			reason := strings.TrimPrefix(status, "error:")
			now := time.Now().UTC()
			d.db.Exec(ctx, `
				UPDATE sessions SET runtime_config = COALESCE(runtime_config, '{}'::jsonb) || $1::jsonb
				WHERE id = $2
			`, func() string { b, _ := json.Marshal(map[string]string{"container_error": reason}); return string(b) }(), sessionID)
			d.updateSessionStatus(ctx, sessionID, "error", nil, &now)
			if d.workflowEngine != nil {
				d.workflowEngine.OnStepCompletion(ctx, sessionID, "error", nil)
			}
			orphaned++
			log.Printf("reconcile: session %s container error: %s", sessionID, reason)
			continue
		}

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

		// Check if session has exceeded its configured timeout.
		var startedAt time.Time
		var runtimeConfigJSON []byte
		err2 := d.db.QueryRow(ctx,
			`SELECT started_at, runtime_config FROM sessions WHERE id = $1`, sessionID,
		).Scan(&startedAt, &runtimeConfigJSON)
		if err2 == nil && len(runtimeConfigJSON) > 0 {
			var rc map[string]any
			if json.Unmarshal(runtimeConfigJSON, &rc) == nil {
				if tf, ok := rc["timeout"]; ok {
					var timeoutSeconds float64
					switch v := tf.(type) {
					case float64:
						timeoutSeconds = v
					case json.Number:
						timeoutSeconds, _ = v.Float64()
					}
					if timeoutSeconds > 0 && time.Since(startedAt) > time.Duration(timeoutSeconds)*time.Second {
						now := time.Now().UTC()
						d.updateSessionStatus(ctx, sessionID, "timeout", nil, &now)
						if d.workflowEngine != nil {
							d.workflowEngine.OnStepCompletion(ctx, sessionID, "timeout", nil)
						}
						orphaned++
						log.Printf("reconcile: timed out session %s (started %s ago, timeout %0.fs)", sessionID, time.Since(startedAt).Round(time.Second), timeoutSeconds)
						continue
					}
				}
			}
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
// Bridge restarts or NATS message drops. It also sweeps orphaned Gate
// sidecar containers whose Skiff containers are gone.
func (d *Dispatcher) ReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.RecoverHandles(ctx)

			// Sweep orphaned Gate containers.
			cleaned, err := d.rt.CleanupOrphanedContainers(ctx, "gate-")
			if err != nil {
				log.Printf("reconcile: error cleaning up orphaned gate containers: %v", err)
			} else if cleaned > 0 {
				log.Printf("reconcile: cleaned up %d orphaned gate container(s)", cleaned)
			}

			// Sweep orphaned dev containers.
			devCleaned, devErr := d.rt.CleanupOrphanedContainers(ctx, "dev-")
			if devErr != nil {
				log.Printf("reconcile: error cleaning up orphaned dev containers: %v", devErr)
			} else if devCleaned > 0 {
				log.Printf("reconcile: cleaned up %d orphaned dev container(s)", devCleaned)
			}
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

// updateWorkflowStepOutputs stores outputs in the workflow_run_steps table for workflow sessions.
func (d *Dispatcher) updateWorkflowStepOutputs(ctx context.Context, sessionID string, outputs map[string]string) {
	outputsJSON, _ := json.Marshal(outputs)
	result, err := d.db.Exec(ctx, `
		UPDATE workflow_run_steps SET outputs = $1 WHERE session_id = $2
	`, outputsJSON, sessionID)
	if err != nil {
		log.Printf("error: updating workflow step outputs for session %s: %v", sessionID, err)
		return
	}

	rowsAffected := result.RowsAffected()
	if rowsAffected == 0 {
		// Session is not part of a workflow, which is normal for regular sessions
		log.Printf("debug: session %s is not part of a workflow (no workflow_run_steps record found)", sessionID)
	} else {
		log.Printf("updated workflow step outputs for session %s: %d field(s)", sessionID, len(outputs))
	}
}
