# Generalized MCP Tool Gateway Design

## Status: Draft

This document designs a generalized MCP tool gateway for Alcove, extending the
existing SCM proxy pattern (GitHub/GitLab API proxying, git credential helper)
to support arbitrary MCP tool servers with per-task tool selection and
operation-level scoping.

---

## 1. Problem Statement

Today, Gate has hardcoded support for two services: GitHub and GitLab. Each has
a bespoke proxy endpoint (`/github/`, `/gitlab/`), a bespoke operation taxonomy
in `scope.go`, and bespoke credential injection in `injectServiceCredential`.
Adding a new tool (Jira, Slack, a custom internal API) requires modifying Gate's
Go code in multiple places.

Claude Code supports MCP (Model Context Protocol) tool servers that run as
child processes. Users want to enable specific MCP tools per task (e.g., "this
task can use GitHub MCP + Jira MCP but not Slack") with operation-level
controls (e.g., "can read Jira issues but not create them").

The design must:
1. Keep the security invariant: Skiff never holds real credentials.
2. Allow users to register custom MCP tools without modifying Gate source.
3. Scope each tool's operations per task (deny by default).
4. Integrate with the existing proxy/credential/scope architecture.

---

## 2. Architecture Overview

```
                                    +-----------+
User -> Bridge API/Dashboard ------>| Ledger DB |
         |                          +-----------+
         |  (task dispatch)             |
         v                             | (tool registry,
    +---------+                        |  credentials)
    |  NATS   |                        |
    +---------+                        |
         |                             |
         v                             |
    +---------+     +------+           |
    |  Skiff  |<--->| Gate |<----------+
    |         |     |      |
    | Claude  |     | MCP  |-----> External APIs
    | Code    |     | Proxy|       (GitHub, GitLab,
    |         |     |      |        Jira, custom)
    | MCP     |     +------+
    | Servers |
    | (stdio) |
    +---------+

MCP servers run INSIDE Skiff but their API calls route THROUGH Gate.
```

### Key insight: MCP servers as proxied clients

MCP tool servers (e.g., `@modelcontextprotocol/server-github`) run as child
processes of Claude Code inside Skiff. These servers make HTTP API calls to
external services. Because Skiff's traffic is forced through Gate (via
`HTTP_PROXY`/`HTTPS_PROXY` and NetworkPolicy), Gate can:

1. Intercept all API calls from MCP servers
2. Classify each call by service and operation
3. Check against the task's scope
4. Inject real credentials before forwarding

This means MCP servers run with **dummy credentials** and Gate handles the real
auth -- exactly the pattern already working for `gh` CLI and `glab` CLI.

### Two categories of MCP tools

**Category A: API-proxied tools** (GitHub, GitLab, Jira, etc.)
- MCP server runs in Skiff with dummy credentials
- Server makes HTTP calls that Gate intercepts
- Gate has a service-specific proxy endpoint and operation taxonomy
- This is the existing pattern, generalized

**Category B: Sidecar-hosted tools** (custom tools needing real credentials)
- MCP server runs inside Gate (not Skiff)
- Claude Code connects to it via Gate's HTTP endpoint
- Gate exposes an MCP-over-HTTP transport endpoint per tool
- The tool server has direct access to real credentials
- This is for tools where credential injection via HTTP proxy is not feasible

Phase 1 focuses on Category A. Category B is designed but deferred.

---

## 3. Data Model

### 3.1 Database Schema

**New table: `mcp_tools`**

```sql
-- 006_mcp_tools.sql

CREATE TABLE IF NOT EXISTS mcp_tools (
    id            UUID PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,      -- e.g., "github", "gitlab", "jira"
    display_name  TEXT NOT NULL,             -- e.g., "GitHub", "GitLab", "Jira"
    tool_type     TEXT NOT NULL DEFAULT 'builtin',  -- "builtin" or "custom"
    transport     TEXT NOT NULL DEFAULT 'api_proxy', -- "api_proxy" or "sidecar"
    description   TEXT,                      -- human-readable description

    -- MCP server configuration (for running inside Skiff)
    mcp_command   TEXT,                      -- e.g., "npx"
    mcp_args      JSONB DEFAULT '[]',        -- e.g., ["-y", "@modelcontextprotocol/server-github"]
    mcp_env       JSONB DEFAULT '{}',        -- env vars the MCP server needs (keys only; values come from credentials)

    -- API proxy configuration (for Gate)
    api_host      TEXT,                      -- e.g., "api.github.com", "gitlab.com"
    api_scheme    TEXT DEFAULT 'https',       -- "https" or "http"
    auth_header   TEXT,                      -- e.g., "Authorization: Bearer", "PRIVATE-TOKEN"
    auth_format   TEXT DEFAULT 'bearer',     -- "bearer", "header", "basic"

    -- Operation taxonomy
    operations    JSONB NOT NULL DEFAULT '{}',  -- operation definitions (see below)

    -- Metadata
    owner         TEXT,                      -- user who registered it (null for builtins)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**New table: `task_tools`** (per-task tool configuration)

```sql
CREATE TABLE IF NOT EXISTS task_tools (
    id            UUID PRIMARY KEY,
    session_id    UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_name     TEXT NOT NULL REFERENCES mcp_tools(name),
    operations    JSONB NOT NULL DEFAULT '[]',  -- allowed operations for this task
    repos         JSONB DEFAULT '[]',            -- allowed repos/projects (for SCM tools)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_tools_session ON task_tools(session_id);
```

### 3.2 Operations JSONB Structure

The `operations` column in `mcp_tools` defines the available operations with
metadata for the scope checker:

```json
{
    "read_prs": {
        "display_name": "Read Pull Requests",
        "description": "List and read pull request details",
        "tier": "read",
        "methods": ["GET"],
        "path_patterns": ["/repos/{owner}/{repo}/pulls/**"]
    },
    "create_pr_draft": {
        "display_name": "Create Draft PR",
        "description": "Create a new draft pull request",
        "tier": "write",
        "methods": ["POST"],
        "path_patterns": ["/repos/{owner}/{repo}/pulls"]
    },
    "merge_pr": {
        "display_name": "Merge Pull Request",
        "description": "Merge a pull request",
        "tier": "dangerous",
        "methods": ["PUT"],
        "path_patterns": ["/repos/{owner}/{repo}/pulls/{n}/merge"]
    },
    "clone": {
        "display_name": "Clone Repository",
        "description": "Clone a repository via git",
        "tier": "read",
        "transport": "git"
    }
}
```

Tiers (`read`, `write`, `dangerous`) are used by the dashboard for grouped
display. They do not affect enforcement -- only the explicit operation list
matters.

### 3.3 Go Types

**File: `internal/types.go`** -- additions

```go
// MCPTool represents a registered MCP tool with its configuration and operation taxonomy.
type MCPTool struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    DisplayName string            `json:"display_name"`
    ToolType    string            `json:"tool_type"`    // "builtin" or "custom"
    Transport   string            `json:"transport"`    // "api_proxy" or "sidecar"
    Description string            `json:"description,omitempty"`

    // MCP server config
    MCPCommand  string            `json:"mcp_command,omitempty"`
    MCPArgs     []string          `json:"mcp_args,omitempty"`
    MCPEnv      map[string]string `json:"mcp_env,omitempty"`

    // API proxy config
    APIHost     string            `json:"api_host,omitempty"`
    APIScheme   string            `json:"api_scheme,omitempty"`
    AuthHeader  string            `json:"auth_header,omitempty"`
    AuthFormat  string            `json:"auth_format,omitempty"`

    // Operations
    Operations  map[string]ToolOperation `json:"operations"`

    Owner       string    `json:"owner,omitempty"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}

// ToolOperation defines a single operation within a tool's taxonomy.
type ToolOperation struct {
    DisplayName  string   `json:"display_name"`
    Description  string   `json:"description,omitempty"`
    Tier         string   `json:"tier"`           // "read", "write", "dangerous"
    Methods      []string `json:"methods,omitempty"`
    PathPatterns []string `json:"path_patterns,omitempty"`
    Transport    string   `json:"transport,omitempty"` // "git" for git transport ops
}

// TaskToolConfig specifies which tools and operations a task is authorized to use.
type TaskToolConfig struct {
    Enabled    bool     `json:"enabled"`
    Repos      []string `json:"repos,omitempty"`
    Operations []string `json:"operations"`
}

// MCPServerConfig is the Claude Code MCP server configuration format.
type MCPServerConfig struct {
    Command string            `json:"command"`
    Args    []string          `json:"args"`
    Env     map[string]string `json:"env"`
}
```

### 3.4 Scope Type Evolution

The existing `Scope` / `ServiceScope` types remain unchanged. The `Services`
map key is the tool name. This means the existing scope enforcement in Gate
continues to work. The `mcp_tools` table provides metadata (display name,
operation taxonomy, proxy config) that Bridge uses when constructing scopes and
Gate uses when proxying.

```go
// Existing -- no changes needed
type Scope struct {
    Services map[string]ServiceScope `json:"services"`
}

type ServiceScope struct {
    Repos      []string `json:"repos,omitempty"`
    Operations []string `json:"operations"`
}
```

The scope JSON in a task request:
```json
{
    "services": {
        "github": {
            "repos": ["org/repo"],
            "operations": ["clone", "push_branch", "create_pr_draft", "read_prs"]
        },
        "jira": {
            "operations": ["read_issues", "create_comment"]
        }
    }
}
```

---

## 4. API Changes

### 4.1 Tool Registry API

**New endpoints:**

```
GET    /api/v1/tools           -- list all registered tools
GET    /api/v1/tools/{name}    -- get tool details + operation taxonomy
POST   /api/v1/tools           -- register a custom tool
PUT    /api/v1/tools/{name}    -- update a custom tool
DELETE /api/v1/tools/{name}    -- delete a custom tool (only custom, not builtin)
```

**List tools response:**
```json
{
    "tools": [
        {
            "name": "github",
            "display_name": "GitHub",
            "tool_type": "builtin",
            "transport": "api_proxy",
            "description": "GitHub API and git operations",
            "operations": {
                "clone":           {"display_name": "Clone", "tier": "read"},
                "read_prs":        {"display_name": "Read PRs", "tier": "read"},
                "create_pr_draft": {"display_name": "Create Draft PR", "tier": "write"},
                "merge_pr":        {"display_name": "Merge PR", "tier": "dangerous"}
            },
            "has_credential": true
        }
    ]
}
```

The `has_credential` field is computed at query time by checking whether a
matching credential exists in `provider_credentials`.

**Register custom tool request:**
```json
{
    "name": "internal-api",
    "display_name": "Internal API",
    "transport": "api_proxy",
    "mcp_command": "npx",
    "mcp_args": ["-y", "@company/mcp-internal-api"],
    "mcp_env": {"API_TOKEN": ""},
    "api_host": "api.internal.company.com",
    "auth_format": "bearer",
    "operations": {
        "read": {
            "display_name": "Read",
            "tier": "read",
            "methods": ["GET", "HEAD"],
            "path_patterns": ["/**"]
        },
        "write": {
            "display_name": "Write",
            "tier": "write",
            "methods": ["POST", "PUT", "PATCH", "DELETE"],
            "path_patterns": ["/**"]
        }
    }
}
```

### 4.2 Task Request Changes

The existing `POST /api/v1/tasks` endpoint's `scope` field continues to work
as-is. An additional `tools` field is added as syntactic sugar that Bridge
resolves into a scope:

```json
{
    "prompt": "Fix the auth bug",
    "provider": "vertex",
    "tools": {
        "github": {
            "enabled": true,
            "repos": ["org/repo"],
            "operations": ["clone", "push_branch", "create_pr_draft", "read_prs"]
        },
        "jira": {
            "enabled": true,
            "operations": ["read_issues", "create_comment"]
        }
    }
}
```

Bridge resolves the `tools` map into a `scope` object. If both `tools` and
`scope` are provided, `tools` takes precedence (they represent the same thing).

**File: `internal/bridge/dispatcher.go`** -- `TaskRequest` changes:

```go
type TaskRequest struct {
    Prompt   string                        `json:"prompt"`
    Repo     string                        `json:"repo,omitempty"`
    Provider string                        `json:"provider,omitempty"`
    Timeout  int                           `json:"timeout,omitempty"`
    Scope    *internal.Scope               `json:"scope,omitempty"`
    Tools    map[string]internal.TaskToolConfig `json:"tools,omitempty"`
    Model    string                        `json:"model,omitempty"`
    Budget   float64                       `json:"budget_usd,omitempty"`
    Debug    bool                          `json:"debug,omitempty"`
}
```

Bridge converts `Tools` to `Scope` early in `DispatchTask`:

```go
if req.Tools != nil {
    scope = resolveToolsToScope(req.Tools)
}
```

```go
func resolveToolsToScope(tools map[string]internal.TaskToolConfig) internal.Scope {
    scope := internal.Scope{Services: make(map[string]internal.ServiceScope)}
    for name, cfg := range tools {
        if !cfg.Enabled {
            continue
        }
        scope.Services[name] = internal.ServiceScope{
            Repos:      cfg.Repos,
            Operations: cfg.Operations,
        }
    }
    return scope
}
```

### 4.3 Credential API

No changes to the credential API. Custom tools store credentials using the
existing `POST /api/v1/credentials` endpoint with the tool name as the
`provider` field:

```json
{
    "name": "jira-prod",
    "provider": "jira",
    "auth_type": "pat",
    "credential": "ATATT3xFfGF0..."
}
```

Gate's existing `AcquireSCMToken` query (`WHERE provider = $1 OR name = $1`)
already supports this.

---

## 5. Gate Proxy Changes

### 5.1 Dynamic Service Registration

Gate currently hardcodes GitHub and GitLab in three places:
1. `isServiceHost()` -- host identification
2. `identifyService()` -- hostname-to-service mapping
3. `injectServiceCredential()` -- credential injection
4. `handleSCMProxy()` -- proxy endpoint handlers
5. `CheckAccess()` / `checkGitHub()` / `checkGitLab()` -- scope checking

The generalized design replaces hardcoded service checks with a registry
loaded from the scope at startup.

**File: `internal/gate/proxy.go`** -- Config changes:

```go
type Config struct {
    // ... existing fields ...

    // ToolConfigs maps tool name -> proxy configuration.
    // Loaded from GATE_TOOL_CONFIGS env var (JSON).
    ToolConfigs map[string]ToolProxyConfig `json:"-"`
}

// ToolProxyConfig tells Gate how to proxy requests for a specific tool.
type ToolProxyConfig struct {
    APIHost    string `json:"api_host"`     // e.g., "api.github.com"
    APIScheme  string `json:"api_scheme"`   // "https" or "http"
    AuthFormat string `json:"auth_format"`  // "bearer", "header", "basic"
    AuthHeader string `json:"auth_header"`  // e.g., "PRIVATE-TOKEN" (for "header" format)
}
```

**New env var: `GATE_TOOL_CONFIGS`**

Set by Bridge when dispatching a task. Contains tool proxy configurations for
all tools in scope:

```json
{
    "github": {
        "api_host": "api.github.com",
        "api_scheme": "https",
        "auth_format": "bearer"
    },
    "gitlab": {
        "api_host": "gitlab.com",
        "api_scheme": "https",
        "auth_format": "header",
        "auth_header": "PRIVATE-TOKEN"
    },
    "jira": {
        "api_host": "mycompany.atlassian.net",
        "api_scheme": "https",
        "auth_format": "bearer"
    }
}
```

### 5.2 Generalized Proxy Endpoint

Replace the hardcoded `/github/` and `/gitlab/` endpoints with a single
pattern that matches any tool name from the scope:

**File: `internal/gate/proxy.go`** -- Handler() changes:

```go
func (p *Proxy) Handler() http.Handler {
    mux := http.NewServeMux()

    // Existing endpoints (git-credential, healthz, /v1/) unchanged...

    // Register a proxy endpoint for each tool in scope
    for toolName := range p.config.ToolConfigs {
        name := toolName // capture for closure
        mux.HandleFunc("/"+name+"/", func(w http.ResponseWriter, r *http.Request) {
            p.handleToolProxy(w, r, name)
        })
    }

    // Backward compatibility: if github/gitlab are in Scope but not
    // ToolConfigs (old-style dispatch), register them with defaults
    for service := range p.config.Scope.Services {
        if _, ok := p.config.ToolConfigs[service]; ok {
            continue // already registered
        }
        if cfg := defaultToolConfig(service); cfg != nil {
            p.config.ToolConfigs[service] = *cfg
            name := service
            mux.HandleFunc("/"+name+"/", func(w http.ResponseWriter, r *http.Request) {
                p.handleToolProxy(w, r, name)
            })
        }
    }

    // ... rest unchanged ...
}
```

The `handleToolProxy` method generalizes `handleSCMProxy`:

```go
func (p *Proxy) handleToolProxy(w http.ResponseWriter, r *http.Request, toolName string) {
    toolCfg, ok := p.config.ToolConfigs[toolName]
    if !ok {
        http.Error(w, "tool not configured", http.StatusInternalServerError)
        return
    }

    prefix := "/" + toolName + "/"
    apiPath := strings.TrimPrefix(r.URL.Path, prefix)
    if apiPath == "" {
        apiPath = "/"
    }

    fakeURL := fmt.Sprintf("%s://%s/%s", toolCfg.APIScheme, toolCfg.APIHost, apiPath)
    if r.URL.RawQuery != "" {
        fakeURL += "?" + r.URL.RawQuery
    }

    result := CheckAccess(r.Method, fakeURL, p.config.Scope, p.config.ToolConfigs)
    if !result.Allowed {
        http.Error(w, "Forbidden: "+result.Reason, http.StatusForbidden)
        p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "deny", http.StatusForbidden)
        return
    }

    targetURL, _ := url.Parse(fakeURL)

    proxy := &httputil.ReverseProxy{
        Director: func(req *http.Request) {
            req.URL = targetURL
            req.Host = toolCfg.APIHost
            p.injectToolCredential(req, toolName, toolCfg)
        },
        FlushInterval: -1,
    }
    proxy.ServeHTTP(w, r)
    p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "allow", http.StatusOK)
}
```

### 5.3 Generalized Credential Injection

Replace the hardcoded `injectServiceCredential` with a format-driven approach:

```go
func (p *Proxy) injectToolCredential(req *http.Request, toolName string, cfg ToolProxyConfig) {
    cred, ok := p.config.Credentials[toolName]
    if !ok {
        return
    }

    switch cfg.AuthFormat {
    case "bearer":
        req.Header.Set("Authorization", "Bearer "+cred)
    case "header":
        // Custom header name (e.g., "PRIVATE-TOKEN" for GitLab)
        if cfg.AuthHeader != "" {
            req.Header.Set(cfg.AuthHeader, cred)
        }
    case "basic":
        req.SetBasicAuth("token", cred)
    }
}
```

### 5.4 Generalized Scope Checking

The current `CheckAccess` function uses hardcoded host-to-service mapping.
The generalized version uses `ToolConfigs` to identify which tool a URL
belongs to:

```go
func CheckAccess(method, rawURL string, scope Scope, toolConfigs map[string]ToolProxyConfig) AccessResult {
    u, err := url.Parse(rawURL)
    if err != nil {
        return AccessResult{Allowed: false, Reason: "invalid URL"}
    }

    host := u.Hostname()

    // Try to match against registered tool configs
    for toolName, cfg := range toolConfigs {
        if host == cfg.APIHost || strings.Contains(host, cfg.APIHost) {
            return checkToolAccess(method, u.Path, scope, toolName)
        }
    }

    // Fallback to legacy hardcoded checks for backward compatibility
    return checkLegacy(method, host, u.Path, scope)
}
```

For builtin tools (GitHub, GitLab), the existing fine-grained operation mapping
(`mapGitHubOperation`, `mapGitLabOperation`) continues to be used. For custom
tools, a generic method-based classification is used unless the tool registration
includes `path_patterns`:

```go
func checkToolAccess(method, path string, scope Scope, toolName string) AccessResult {
    svcScope, ok := scope.Services[toolName]
    if !ok {
        return AccessResult{Allowed: false, Service: toolName, Reason: toolName + " not in scope"}
    }

    // Use builtin mappers for known tools
    var op string
    switch toolName {
    case "github":
        op = classifyGitHub(method, path)
    case "gitlab":
        op = classifyGitLab(method, path)
    default:
        // Generic: read for GET/HEAD, write for everything else
        if method == "GET" || method == "HEAD" {
            op = "read"
        } else {
            op = "write"
        }
    }

    if !operationAllowed(op, svcScope.Operations) {
        return AccessResult{
            Allowed:   false,
            Service:   toolName,
            Operation: op,
            Reason:    fmt.Sprintf("operation %q not permitted", op),
        }
    }
    return AccessResult{Allowed: true, Service: toolName, Operation: op}
}
```

### 5.5 CONNECT Tunnel Changes

The `handleConnect` method needs to recognize custom tool hosts. Update
`isServiceHost` and `identifyService` to use the tool configs:

```go
func (p *Proxy) isRegisteredToolHost(hostname string) (string, bool) {
    for toolName, cfg := range p.config.ToolConfigs {
        if hostname == cfg.APIHost || strings.HasSuffix(hostname, "."+cfg.APIHost) {
            return toolName, true
        }
    }
    return "", false
}
```

In `handleConnect`, use this instead of `isServiceHost`:

```go
case hostname == "api.github.com":
    // Block -- must use /github/ proxy endpoint
    ...
default:
    if toolName, ok := p.isRegisteredToolHost(hostname); ok {
        // Block CONNECT to tool API hosts -- must use /<tool>/ proxy endpoint
        http.Error(w, fmt.Sprintf("Forbidden: use /%s/ proxy endpoint", toolName), http.StatusForbidden)
        return
    }
```

---

## 6. Skiff-init Changes for MCP Configuration

### 6.1 MCP Config Generation

Claude Code reads MCP server configuration from `~/.claude.json` under the
`mcpServers` key. Bridge generates this config and passes it to Skiff via
the `ALCOVE_MCP_CONFIG` environment variable. Skiff-init writes it to disk
before starting Claude Code.

**File: `cmd/skiff-init/main.go`** -- new function:

```go
// writeMCPConfig writes Claude Code's MCP server configuration.
// The config is passed via ALCOVE_MCP_CONFIG env var as JSON.
func writeMCPConfig(gateURL string) error {
    mcpConfigJSON := os.Getenv("ALCOVE_MCP_CONFIG")
    if mcpConfigJSON == "" {
        return nil // no MCP tools configured
    }

    var mcpServers map[string]internal.MCPServerConfig
    if err := json.Unmarshal([]byte(mcpConfigJSON), &mcpServers); err != nil {
        return fmt.Errorf("parsing ALCOVE_MCP_CONFIG: %w", err)
    }

    // Write the config where Claude Code reads it.
    // Claude Code supports a managed MCP config file.
    claudeConfig := map[string]any{
        "mcpServers": mcpServers,
    }

    configPath := filepath.Join(os.Getenv("HOME"), ".claude.json")
    data, err := json.MarshalIndent(claudeConfig, "", "  ")
    if err != nil {
        return fmt.Errorf("marshaling MCP config: %w", err)
    }

    return os.WriteFile(configPath, data, 0644)
}
```

Called from `main()` after `setupEnv()` and before `runClaude()`:

```go
if err := writeMCPConfig(os.Getenv("ANTHROPIC_BASE_URL")); err != nil {
    log.Printf("warning: failed to write MCP config: %v", err)
}
```

### 6.2 MCP Config Format

The `ALCOVE_MCP_CONFIG` env var contains a JSON object mapping server names
to their configs. Each MCP server's environment variables point to Gate:

```json
{
    "github": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": {
            "GITHUB_PERSONAL_ACCESS_TOKEN": "alcove-session-<uuid>",
            "GITHUB_API_URL": "http://gate-<taskID>:8443/github"
        }
    },
    "gitlab": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-gitlab"],
        "env": {
            "GITLAB_PERSONAL_ACCESS_TOKEN": "alcove-session-<uuid>",
            "GITLAB_API_URL": "http://gate-<taskID>:8443/gitlab/api/v4"
        }
    }
}
```

### 6.3 How Bridge Builds MCP Config

In `DispatchTask`, after resolving credentials and building the scope, Bridge
queries the `mcp_tools` table for each tool in scope and builds the MCP config:

```go
func (d *Dispatcher) buildMCPConfig(ctx context.Context, scope internal.Scope, gateName string, dummyTokens map[string]string) (string, error) {
    mcpServers := make(map[string]internal.MCPServerConfig)

    for toolName := range scope.Services {
        tool, err := d.getToolByName(ctx, toolName)
        if err != nil {
            log.Printf("warning: tool %q not found in registry: %v", toolName, err)
            continue
        }
        if tool.MCPCommand == "" {
            continue // tool has no MCP server (e.g., git-only)
        }

        // Build env vars for the MCP server
        env := make(map[string]string)
        for k, v := range tool.MCPEnv {
            if v == "" {
                // Empty value means "fill with dummy token or proxy URL"
                // Convention: keys ending in _TOKEN or _KEY get the dummy token
                // Keys ending in _URL or _HOST get the Gate proxy URL
                if strings.HasSuffix(k, "_TOKEN") || strings.HasSuffix(k, "_KEY") ||
                   strings.HasSuffix(k, "_ACCESS_TOKEN") {
                    env[k] = dummyTokens[toolName]
                } else if strings.HasSuffix(k, "_URL") || strings.HasSuffix(k, "_HOST") {
                    env[k] = fmt.Sprintf("http://%s:8443/%s", gateName, toolName)
                }
            } else {
                env[k] = v
            }
        }

        mcpServers[toolName] = internal.MCPServerConfig{
            Command: tool.MCPCommand,
            Args:    tool.MCPArgs,
            Env:     env,
        }
    }

    if len(mcpServers) == 0 {
        return "", nil
    }

    data, err := json.Marshal(mcpServers)
    return string(data), err
}
```

---

## 7. Bridge Dispatcher Changes

### 7.1 Enhanced DispatchTask Flow

The existing `DispatchTask` method grows with MCP tool support. The changes
are additive:

```go
func (d *Dispatcher) DispatchTask(ctx context.Context, req TaskRequest, submitter string) (*internal.Session, error) {
    // ... existing session creation, provider resolution ...

    // NEW: Convert tools to scope if provided
    if req.Tools != nil {
        scope = resolveToolsToScope(req.Tools)
    }

    // NEW: Expand operation aliases
    scope = expandAliases(scope)

    // ... existing LLM credential acquisition ...

    // CHANGED: Generalized credential resolution
    // Instead of hardcoded github/gitlab, iterate all tools in scope
    scmCredentials := make(map[string]string)
    scmDummyTokens := make(map[string]string)
    toolConfigs := make(map[string]gate.ToolProxyConfig)

    for toolName := range scope.Services {
        // Look up tool config from registry
        tool, err := d.getToolByName(ctx, toolName)
        if err != nil {
            log.Printf("warning: tool %q not in registry, trying credential lookup", toolName)
        }

        // Acquire credential
        realToken, err := d.credStore.AcquireSCMToken(ctx, toolName)
        if err != nil {
            log.Printf("warning: no credential for %s: %v", toolName, err)
            continue
        }
        scmCredentials[toolName] = realToken
        dummyToken := "alcove-session-" + uuid.New().String()
        scmDummyTokens[toolName] = dummyToken

        // Build tool proxy config for Gate
        if tool != nil {
            toolConfigs[toolName] = gate.ToolProxyConfig{
                APIHost:    tool.APIHost,
                APIScheme:  tool.APIScheme,
                AuthFormat: tool.AuthFormat,
                AuthHeader: tool.AuthHeader,
            }
        }
    }

    // Set Gate env vars
    if len(scmCredentials) > 0 {
        credJSON, _ := json.Marshal(scmCredentials)
        gateEnv["GATE_CREDENTIALS"] = string(credJSON)
    }
    if len(toolConfigs) > 0 {
        toolConfigsJSON, _ := json.Marshal(toolConfigs)
        gateEnv["GATE_TOOL_CONFIGS"] = string(toolConfigsJSON)
    }

    // NEW: Set well-known env vars for builtin tools (backward compat)
    setBuiltinToolEnvVars(skiffEnv, scmDummyTokens, gateName)

    // NEW: Build MCP config
    mcpConfig, err := d.buildMCPConfig(ctx, scope, gateName, scmDummyTokens)
    if err != nil {
        log.Printf("warning: failed to build MCP config: %v", err)
    }
    if mcpConfig != "" {
        skiffEnv["ALCOVE_MCP_CONFIG"] = mcpConfig
    }

    // ... rest unchanged (Runtime.RunTask, etc.) ...
}
```

### 7.2 Alias Expansion

```go
func expandAliases(scope internal.Scope) internal.Scope {
    expanded := internal.Scope{Services: make(map[string]internal.ServiceScope)}
    for name, svc := range scope.Services {
        ops := expandOperationAliases(svc.Operations)
        expanded.Services[name] = internal.ServiceScope{
            Repos:      svc.Repos,
            Operations: ops,
        }
    }
    return expanded
}

var operationAliases = map[string][]string{
    "read_all":   {"read", "read_prs", "read_issues", "read_contents", "read_commits",
                   "read_branches", "read_actions", "read_releases", "read_git", "clone", "fetch"},
    "contribute": {"read_all", "push_branch", "create_pr_draft", "create_comment"},
    "maintain":   {"contribute", "create_pr", "merge_pr", "create_branch"},
}

func expandOperationAliases(ops []string) []string {
    seen := make(map[string]bool)
    var result []string
    var expand func(op string)
    expand = func(op string) {
        if seen[op] {
            return
        }
        if aliases, ok := operationAliases[op]; ok {
            seen[op] = true
            for _, a := range aliases {
                expand(a)
            }
        } else {
            seen[op] = true
            result = append(result, op)
        }
    }
    for _, op := range ops {
        expand(op)
    }
    return result
}
```

---

## 8. Builtin Tool Seed Data

Bridge seeds the `mcp_tools` table on startup with builtin tool definitions.
This runs as part of database migration or as application-level seed logic.

**File: `internal/bridge/tools_seed.go`**

```go
var builtinTools = []internal.MCPTool{
    {
        Name:        "github",
        DisplayName: "GitHub",
        ToolType:    "builtin",
        Transport:   "api_proxy",
        Description: "GitHub API access and git operations",
        MCPCommand:  "npx",
        MCPArgs:     []string{"-y", "@modelcontextprotocol/server-github"},
        MCPEnv:      map[string]string{
            "GITHUB_PERSONAL_ACCESS_TOKEN": "",
            "GITHUB_API_URL": "",
        },
        APIHost:    "api.github.com",
        APIScheme:  "https",
        AuthFormat: "bearer",
        Operations: map[string]internal.ToolOperation{
            "clone":            {DisplayName: "Clone", Tier: "read", Transport: "git"},
            "fetch":            {DisplayName: "Fetch", Tier: "read", Transport: "git"},
            "push_branch":      {DisplayName: "Push Branch", Tier: "write", Transport: "git"},
            "push_main":        {DisplayName: "Push Main", Tier: "dangerous", Transport: "git"},
            "read":             {DisplayName: "Read (general)", Tier: "read", Methods: []string{"GET"}},
            "read_prs":         {DisplayName: "Read PRs", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/pulls/**"}},
            "read_issues":      {DisplayName: "Read Issues", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/issues/**"}},
            "read_contents":    {DisplayName: "Read Contents", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/contents/**"}},
            "read_commits":     {DisplayName: "Read Commits", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/commits/**"}},
            "read_branches":    {DisplayName: "Read Branches", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/branches/**"}},
            "read_actions":     {DisplayName: "Read Actions", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/actions/**"}},
            "read_releases":    {DisplayName: "Read Releases", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/releases/**"}},
            "read_git":         {DisplayName: "Read Git Refs", Tier: "read", Methods: []string{"GET"}, PathPatterns: []string{"/repos/{o}/{r}/git/**"}},
            "create_pr_draft":  {DisplayName: "Create Draft PR", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/pulls"}},
            "create_pr":        {DisplayName: "Create PR", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/pulls"}},
            "update_pr":        {DisplayName: "Update PR", Tier: "write", Methods: []string{"PATCH"}, PathPatterns: []string{"/repos/{o}/{r}/pulls/{n}"}},
            "create_issue":     {DisplayName: "Create Issue", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/issues"}},
            "update_issue":     {DisplayName: "Update Issue", Tier: "write", Methods: []string{"PATCH"}, PathPatterns: []string{"/repos/{o}/{r}/issues/{n}"}},
            "create_comment":   {DisplayName: "Create Comment", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/issues/{n}/comments"}},
            "create_review":    {DisplayName: "Create Review", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/pulls/{n}/reviews"}},
            "create_branch":    {DisplayName: "Create Branch", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/git/refs"}},
            "write_contents":   {DisplayName: "Write Contents", Tier: "write", Methods: []string{"PUT", "POST"}, PathPatterns: []string{"/repos/{o}/{r}/contents/**"}},
            "write_git":        {DisplayName: "Write Git Refs", Tier: "write", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/git/**"}},
            "merge_pr":         {DisplayName: "Merge PR", Tier: "dangerous", Methods: []string{"PUT"}, PathPatterns: []string{"/repos/{o}/{r}/pulls/{n}/merge"}},
            "delete_branch":    {DisplayName: "Delete Branch", Tier: "dangerous", Methods: []string{"DELETE"}, PathPatterns: []string{"/repos/{o}/{r}/git/refs/**"}},
            "write_actions":    {DisplayName: "Write Actions", Tier: "dangerous", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/actions/**"}},
            "write_releases":   {DisplayName: "Write Releases", Tier: "dangerous", Methods: []string{"POST"}, PathPatterns: []string{"/repos/{o}/{r}/releases"}},
            "write":            {DisplayName: "Write (catch-all)", Tier: "dangerous", Methods: []string{"POST", "PUT", "PATCH", "DELETE"}},
        },
    },
    {
        Name:        "gitlab",
        DisplayName: "GitLab",
        ToolType:    "builtin",
        Transport:   "api_proxy",
        Description: "GitLab API access and git operations",
        MCPCommand:  "npx",
        MCPArgs:     []string{"-y", "@modelcontextprotocol/server-gitlab"},
        MCPEnv:      map[string]string{
            "GITLAB_PERSONAL_ACCESS_TOKEN": "",
            "GITLAB_API_URL": "",
        },
        APIHost:    "gitlab.com",
        APIScheme:  "https",
        AuthFormat: "header",
        AuthHeader: "PRIVATE-TOKEN",
        Operations: map[string]internal.ToolOperation{
            "clone":            {DisplayName: "Clone", Tier: "read", Transport: "git"},
            "fetch":            {DisplayName: "Fetch", Tier: "read", Transport: "git"},
            "push_branch":      {DisplayName: "Push Branch", Tier: "write", Transport: "git"},
            "push_main":        {DisplayName: "Push Main", Tier: "dangerous", Transport: "git"},
            "read":             {DisplayName: "Read (general)", Tier: "read", Methods: []string{"GET"}},
            "read_prs":         {DisplayName: "Read MRs", Tier: "read"},
            "read_issues":      {DisplayName: "Read Issues", Tier: "read"},
            "read_contents":    {DisplayName: "Read Contents", Tier: "read"},
            "read_commits":     {DisplayName: "Read Commits", Tier: "read"},
            "read_branches":    {DisplayName: "Read Branches", Tier: "read"},
            "read_actions":     {DisplayName: "Read Pipelines", Tier: "read"},
            "read_releases":    {DisplayName: "Read Releases", Tier: "read"},
            "create_pr_draft":  {DisplayName: "Create Draft MR", Tier: "write"},
            "create_pr":        {DisplayName: "Create MR", Tier: "write"},
            "update_pr":        {DisplayName: "Update MR", Tier: "write"},
            "create_issue":     {DisplayName: "Create Issue", Tier: "write"},
            "update_issue":     {DisplayName: "Update Issue", Tier: "write"},
            "create_comment":   {DisplayName: "Create Comment", Tier: "write"},
            "create_review":    {DisplayName: "Approve MR", Tier: "write"},
            "create_branch":    {DisplayName: "Create Branch", Tier: "write"},
            "write_contents":   {DisplayName: "Write Contents", Tier: "write"},
            "merge_pr":         {DisplayName: "Merge MR", Tier: "dangerous"},
            "delete_branch":    {DisplayName: "Delete Branch", Tier: "dangerous"},
            "write_actions":    {DisplayName: "Write Pipelines", Tier: "dangerous"},
            "write_releases":   {DisplayName: "Write Releases", Tier: "dangerous"},
            "write":            {DisplayName: "Write (catch-all)", Tier: "dangerous"},
        },
    },
}
```

---

## 9. Git Transport Integration

### 9.1 Credential Helper

The existing git credential helper (`build/alcove-credential-helper`) and
Gate's `/git-credential` endpoint continue to work unchanged. The credential
helper works for any git host -- it sends the host to Gate, and Gate checks
the scope to find the matching service.

The `HandleGitCredential` function in `scope.go` needs one change: instead
of hardcoding `github.com` and `gitlab.*`, it should match against
registered tool configs:

```go
func HandleGitCredential(w http.ResponseWriter, r *http.Request, scope internal.Scope,
    credentials map[string]string, toolConfigs map[string]ToolProxyConfig) {
    // ... parse input fields ...

    // Match git host to a registered tool
    var service string
    for toolName, cfg := range toolConfigs {
        // Git transport uses the main domain, not the API domain
        // e.g., github.com (not api.github.com), gitlab.com
        gitHost := cfg.APIHost
        // Strip "api." prefix for GitHub-style APIs
        gitHost = strings.TrimPrefix(gitHost, "api.")
        if strings.Contains(host, gitHost) {
            service = toolName
            break
        }
    }
    if service == "" {
        http.Error(w, "unknown git host", http.StatusForbidden)
        return
    }

    // ... rest of credential check and response unchanged ...
}
```

### 9.2 Read-Only vs Read-Write Credentials

When only `clone`/`fetch` are in scope (no `push_*`), Gate should ideally
issue a read-only credential. This is currently deferred (see
`gate-scm-authorization.md` section 2.3). The MCP tool gateway design does
not change this -- read-only enforcement still depends on the token type
(GitHub App installation tokens can be scoped; PATs cannot).

---

## 10. Dashboard UI Requirements

### 10.1 Tool Selection Panel

The task creation form gets a new "Tools" panel that:

1. **Lists available tools** from `GET /api/v1/tools`, grouped by type:
   - Builtin tools (GitHub, GitLab) shown first with icons
   - Custom tools shown below with a generic icon
   - Each tool shows whether a credential is configured (green check / red X)

2. **Tool toggle**: each tool has an enable/disable toggle

3. **Operation picker** (per tool when enabled):
   - Operations grouped by tier (Read / Write / Dangerous)
   - Tier-level "select all" checkboxes
   - Preset buttons: "Read-Only", "Contributor", "Maintainer"
   - Individual operation checkboxes with descriptions

4. **Repository scope** (for SCM tools): text input for repo patterns
   - Supports wildcards (`org/*`, `*`)
   - Autocomplete from credential's accessible repos (future)

5. **Scope preview**: collapsible JSON viewer showing the computed scope

### 10.2 Tool Registry Page

A new "Tools" page in the dashboard for managing the tool registry:

1. **List view**: table of all registered tools (builtin + custom)
2. **Detail view**: tool configuration + operation taxonomy
3. **Register form**: for adding custom tools
4. **Credential status**: shows which tools have credentials configured

### 10.3 Session Detail Enhancements

The session detail page shows:
- Which tools were enabled for the task
- Proxy log filtered by tool
- Operation counts by tool (N reads, N writes, N denials)

---

## 11. File-by-File Implementation Plan

### Phase 1: Tool Registry (no runtime changes)

| # | File | Change | Depends On |
|---|------|--------|------------|
| 1.1 | `internal/types.go` | Add `MCPTool`, `ToolOperation`, `TaskToolConfig`, `MCPServerConfig` types | -- |
| 1.2 | `internal/bridge/migrations/006_mcp_tools.sql` | Create `mcp_tools` and `task_tools` tables | -- |
| 1.3 | `internal/bridge/tools.go` (new) | `ToolStore` with CRUD operations: `CreateTool`, `GetTool`, `ListTools`, `UpdateTool`, `DeleteTool` | 1.1, 1.2 |
| 1.4 | `internal/bridge/tools_seed.go` (new) | `SeedBuiltinTools()` function with GitHub and GitLab definitions | 1.1, 1.3 |
| 1.5 | `internal/bridge/tools_test.go` (new) | Unit tests for ToolStore CRUD | 1.3 |
| 1.6 | `internal/bridge/api.go` | Add `/api/v1/tools` and `/api/v1/tools/{name}` handlers | 1.3 |
| 1.7 | `internal/bridge/api_test.go` | Tests for tool API endpoints | 1.6 |
| 1.8 | `cmd/bridge/main.go` | Call `SeedBuiltinTools()` after migration, wire `ToolStore` | 1.4 |

**Parallelism:** 1.1 and 1.2 can be done in parallel. 1.3 depends on both.
1.4-1.5 depend on 1.3. 1.6-1.7 depend on 1.3. 1.8 depends on 1.4 and 1.6.

### Phase 2: Tools-to-Scope Resolution + MCP Config

| # | File | Change | Depends On |
|---|------|--------|------------|
| 2.1 | `internal/bridge/dispatcher.go` | Add `Tools` field to `TaskRequest`, add `resolveToolsToScope()` | 1.1 |
| 2.2 | `internal/bridge/scope_expand.go` (new) | `expandAliases()` and `expandOperationAliases()` | -- |
| 2.3 | `internal/bridge/scope_expand_test.go` (new) | Tests for alias expansion | 2.2 |
| 2.4 | `internal/bridge/dispatcher.go` | Add `buildMCPConfig()` method | 1.3 |
| 2.5 | `internal/bridge/dispatcher.go` | Generalize credential resolution loop (replace hardcoded github/gitlab) | 1.3 |
| 2.6 | `internal/bridge/dispatcher.go` | Build and set `GATE_TOOL_CONFIGS` env var | 2.5 |
| 2.7 | `internal/bridge/dispatcher_test.go` | Tests for tools-to-scope resolution and MCP config | 2.1, 2.4, 2.6 |

**Parallelism:** 2.1 and 2.2 can be done in parallel. 2.4, 2.5, 2.6 are
sequential. 2.7 depends on all.

### Phase 3: Gate Generalization

| # | File | Change | Depends On |
|---|------|--------|------------|
| 3.1 | `internal/gate/proxy.go` | Add `ToolProxyConfig` type, `ToolConfigs` to Config | -- |
| 3.2 | `internal/gate/proxy.go` | Add `handleToolProxy()` method (generalized `handleSCMProxy`) | 3.1 |
| 3.3 | `internal/gate/proxy.go` | Add `injectToolCredential()` (generalized `injectServiceCredential`) | 3.1 |
| 3.4 | `internal/gate/proxy.go` | Update `Handler()` to register dynamic tool endpoints | 3.2, 3.3 |
| 3.5 | `internal/gate/scope.go` | Update `CheckAccess` to accept `ToolConfigs`, add `checkToolAccess` | 3.1 |
| 3.6 | `internal/gate/scope.go` | Update `HandleGitCredential` to use tool configs for host matching | 3.1 |
| 3.7 | `internal/gate/proxy.go` | Update `handleConnect` to block CONNECT to registered tool API hosts | 3.1 |
| 3.8 | `internal/gate/proxy.go` | Backward compat: `defaultToolConfig()` for github/gitlab when `ToolConfigs` is empty | 3.4 |
| 3.9 | `cmd/gate/main.go` | Load `GATE_TOOL_CONFIGS` env var | 3.1 |
| 3.10 | `internal/gate/proxy_test.go` | Tests for generalized tool proxy | 3.4, 3.5 |
| 3.11 | `internal/gate/scope_test.go` | Tests for generalized scope checking | 3.5, 3.6 |

**Parallelism:** 3.1 is the foundation. 3.2, 3.3, 3.5, 3.6, 3.7 can all be
done in parallel once 3.1 is complete. 3.4 depends on 3.2 and 3.3. 3.8 depends
on 3.4. Tests (3.10, 3.11) depend on their subjects.

### Phase 4: Skiff MCP Config Writer

| # | File | Change | Depends On |
|---|------|--------|------------|
| 4.1 | `cmd/skiff-init/main.go` | Add `writeMCPConfig()` function | -- |
| 4.2 | `cmd/skiff-init/main.go` | Call `writeMCPConfig()` in `main()` before `runClaude()` | 4.1 |
| 4.3 | `cmd/skiff-init/main_test.go` | Test MCP config file writing | 4.1 |

**Parallelism:** 4.1-4.3 are independent of Phases 1-3 (they only depend on
the env var contract). Can be done in parallel with Phase 2 and 3.

### Phase 5: Dashboard UI

| # | File | Change | Depends On |
|---|------|--------|------------|
| 5.1 | `web/static/` | Tool selection panel in task creation form | Phase 1 (API) |
| 5.2 | `web/static/` | Operation picker with tier grouping and presets | 5.1 |
| 5.3 | `web/static/` | Tool registry management page | Phase 1 (API) |
| 5.4 | `web/static/` | Scope preview JSON viewer | 5.2 |
| 5.5 | `web/static/` | Session detail tool/proxy-log enhancements | -- |

**Parallelism:** 5.1 and 5.3 can be done in parallel. 5.2 and 5.4 depend on
5.1. 5.5 is independent.

### Phase 6: Integration Testing

| # | Description | Depends On |
|---|-------------|------------|
| 6.1 | End-to-end test: task with GitHub tool via MCP | Phases 2, 3, 4 |
| 6.2 | End-to-end test: task with custom tool | Phases 2, 3, 4 |
| 6.3 | End-to-end test: operation denial and audit logging | Phase 3 |
| 6.4 | Backward compatibility test: old-style scope (no tools field) | Phase 3 |

---

## 12. Task Ordering Summary

```
Phase 1 (Tool Registry)      Phase 4 (Skiff MCP)
       |                            |
       v                            v
Phase 2 (Bridge Resolution)  [can parallel]
       |
       v
Phase 3 (Gate Generalization)
       |
       v
Phase 5 (Dashboard)
       |
       v
Phase 6 (Integration Tests)
```

Phases 1 and 4 can be done in parallel (different components, no dependency).
Phase 2 depends on Phase 1 (needs tool registry to build MCP configs).
Phase 3 depends on Phase 2 (needs `GATE_TOOL_CONFIGS` env var contract).
Phase 5 depends on Phase 1 (needs tool API).
Phase 6 depends on Phases 2, 3, and 4.

---

## 13. Backward Compatibility

All changes are backward compatible:

1. **Old-style `scope` in task requests**: continues to work. The `Tools` field
   is optional. If only `scope` is provided, it passes through unchanged.

2. **Gate without `GATE_TOOL_CONFIGS`**: falls back to the existing hardcoded
   behavior via `defaultToolConfig()`.

3. **No MCP tools in scope**: `ALCOVE_MCP_CONFIG` is empty, `writeMCPConfig()`
   is a no-op, Claude Code runs without MCP servers (current behavior).

4. **Existing GitHub/GitLab env vars**: `GITHUB_TOKEN`, `GITLAB_TOKEN`, etc.
   continue to be set for backward compatibility with `gh` CLI, `glab` CLI,
   and any scripts that read them directly.

---

## 14. Security Considerations

### 14.1 Custom Tool Trust

Custom MCP tools run inside Skiff (not Gate). This means:
- They cannot access real credentials (only dummy tokens)
- Their network traffic is proxied through Gate
- They cannot bypass scope enforcement

However, custom MCP servers are arbitrary executables. They could:
- Consume excessive CPU/memory (mitigated by container resource limits)
- Attempt to exfiltrate data via allowed channels (mitigated by scope)
- Interfere with Claude Code's execution (mitigated by process isolation)

For Phase 1, custom tool registration is restricted to admin users.

### 14.2 Tool Config Injection

The `GATE_TOOL_CONFIGS` env var contains the API host for each tool. If an
attacker could modify this, they could redirect Gate's proxied requests to a
malicious host. This is not a risk because:
- The env var is set by Bridge (trusted component)
- Skiff cannot modify Gate's env vars (separate container/process)
- The env var is loaded once at Gate startup

### 14.3 MCP Config Integrity

The `ALCOVE_MCP_CONFIG` env var is set by Bridge and written to disk by
skiff-init before Claude Code starts. Claude Code reads it as its MCP
server configuration. Since this runs in an ephemeral container and the
env var is injected by Bridge, there is no attack vector.

### 14.4 Operation Taxonomy Completeness

For builtin tools (GitHub, GitLab), the operation taxonomy is maintained in
code and covers the full API surface. For custom tools, the taxonomy is
user-defined. If a user defines only `read` and `write` operations, the
scope enforcement is coarse-grained (method-based only). This is acceptable
because the user chose this granularity when registering the tool.

---

## 15. Open Questions

1. **MCP server installation in Skiff**: The current design uses `npx -y` to
   download MCP servers at runtime. This requires npm network access during
   task execution. Alternative: pre-install known MCP servers in the Skiff
   base image. Trade-off: image size vs. startup latency vs. freshness.

2. **Category B (sidecar-hosted) timeline**: Some MCP tools may not support
   configuring a custom API base URL. For these, the MCP server must run
   inside Gate where it has access to real credentials. This requires an
   MCP-over-HTTP bridge in Gate. Defer to Phase 2?

3. **Tool versioning**: Should the tool registry track MCP server versions?
   Users might want to pin `@modelcontextprotocol/server-github@1.2.3`.

4. **Multi-tenant tool isolation**: In a multi-user deployment, should custom
   tools be visible to all users or only the registering user? Current design:
   admin-only registration, visible to all.

5. **Operation taxonomy for new builtin tools**: When adding Jira, Slack, etc.
   as builtins, who defines the operation taxonomy? Should we ship a minimal
   taxonomy (read/write) and let users refine it?
