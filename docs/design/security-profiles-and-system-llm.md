# Security Profiles, Bridge System LLM, and GitLab Private Server Support

## Status: Draft

This document designs three related features:
1. **Reusable Security Profiles** -- named bundles of tool + repo + operation permissions that replace per-task configuration
2. **Bridge System LLM** -- a system-level LLM connection for AI-powered features inside Bridge itself
3. **GitLab Private Server Support** -- enabling GitLab tools to work with self-hosted instances

---

## 1. Security Profiles

### 1.1 Problem

Today, every task request must specify its full tool configuration inline:

```json
{
    "prompt": "Fix the auth bug",
    "tools": {
        "github": {
            "enabled": true,
            "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
            "operations": ["clone", "push_branch", "create_pr_draft", "read_prs", "create_comment"]
        }
    }
}
```

This is tedious and error-prone. Users repeatedly configure the same tool + repo + operation combinations. There is no way to share permission sets across tasks or enforce organizational defaults.

### 1.2 Design Overview

A **security profile** is a named, reusable, per-user bundle of tool permissions. Profiles are stored in the database and referenced by name at task submission time. Multiple profiles can be stacked on a single task; their scopes merge via union.

### 1.3 Database Schema

**New table: `security_profiles`**

```sql
-- 007_security_profiles.sql

CREATE TABLE IF NOT EXISTS security_profiles (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    tools        JSONB NOT NULL DEFAULT '{}',   -- map[tool_name] -> {repos, operations}
    owner        TEXT NOT NULL DEFAULT '',       -- user who created this profile
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Each user has unique profile names; system profiles have owner=''.
CREATE UNIQUE INDEX IF NOT EXISTS idx_security_profiles_name_owner
    ON security_profiles(name, owner);

-- For listing a user's profiles + system profiles.
CREATE INDEX IF NOT EXISTS idx_security_profiles_owner
    ON security_profiles(owner);
```

The `tools` column stores the exact same structure as the `tools` field in
`TaskRequest`, keyed by tool name:

```json
{
    "github": {
        "enabled": true,
        "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
        "operations": ["clone", "push_branch", "create_pr_draft", "read_prs", "create_comment"]
    },
    "gitlab": {
        "enabled": true,
        "repos": ["myorg/infrastructure"],
        "operations": ["clone", "read_mrs", "read_issues"]
    }
}
```

### 1.4 Go Types

**File: `internal/bridge/profiles.go`**

```go
// SecurityProfile is a named, reusable bundle of tool + repo + operation permissions.
type SecurityProfile struct {
    ID          string                `json:"id"`
    Name        string                `json:"name"`
    DisplayName string                `json:"display_name"`
    Description string                `json:"description"`
    Tools       map[string]ToolConfig `json:"tools"`
    Owner       string                `json:"owner,omitempty"`
    CreatedAt   time.Time             `json:"created_at"`
    UpdatedAt   time.Time             `json:"updated_at"`
}
```

`ToolConfig` is the existing type from `dispatcher.go`:

```go
type ToolConfig struct {
    Enabled    bool     `json:"enabled"`
    Repos      []string `json:"repos,omitempty"`
    Operations []string `json:"operations,omitempty"`
}
```

### 1.5 ProfileStore

**File: `internal/bridge/profiles.go`**

```go
type ProfileStore struct {
    db *pgxpool.Pool
}

func NewProfileStore(db *pgxpool.Pool) *ProfileStore

// CRUD
func (ps *ProfileStore) CreateProfile(ctx, profile, owner) error
func (ps *ProfileStore) GetProfile(ctx, name, owner) (*SecurityProfile, error)
func (ps *ProfileStore) ListProfiles(ctx, owner) ([]SecurityProfile, error)
func (ps *ProfileStore) UpdateProfile(ctx, profile, owner) error
func (ps *ProfileStore) DeleteProfile(ctx, name, owner) error
```

`ListProfiles` returns both the user's own profiles AND system profiles
(owner=''), similar to how `ListTools` returns builtins + user tools:

```sql
SELECT ... FROM security_profiles
WHERE owner = $1 OR owner = ''
ORDER BY owner ASC, name ASC
```

`GetProfile` resolves with user-first priority: if a user has a profile
named "contributor" and a system profile named "contributor" also exists,
the user's profile wins.

### 1.6 Profile Stacking (Merge Semantics)

When a task request specifies `"profiles": ["pulp-contributor", "code-reader"]`,
Bridge resolves each profile and merges their `tools` maps using **union**
semantics:

```go
func MergeProfiles(profiles []SecurityProfile) map[string]ToolConfig {
    merged := make(map[string]ToolConfig)
    for _, profile := range profiles {
        for toolName, toolCfg := range profile.Tools {
            if !toolCfg.Enabled {
                continue
            }
            existing, ok := merged[toolName]
            if !ok {
                merged[toolName] = ToolConfig{
                    Enabled:    true,
                    Repos:      toolCfg.Repos,
                    Operations: toolCfg.Operations,
                }
                continue
            }
            // Union repos
            existing.Repos = unionStrings(existing.Repos, toolCfg.Repos)
            // Union operations
            existing.Operations = unionStrings(existing.Operations, toolCfg.Operations)
            merged[toolName] = existing
        }
    }
    return merged
}
```

Rules:
- **Repos**: union of all repo lists. If any profile specifies `["*"]`, the
  merged result is `["*"]`.
- **Operations**: union of all operation lists. If any profile specifies
  `["*"]`, the merged result is `["*"]`.
- **Tool enablement**: a tool is enabled if ANY stacked profile enables it.
- **Disabled tools in a profile**: `"enabled": false` entries are ignored
  (they do not subtract from other profiles). Profiles are additive only.

### 1.7 TaskRequest Changes

**File: `internal/bridge/dispatcher.go`**

```go
type TaskRequest struct {
    Prompt   string                `json:"prompt"`
    Repo     string                `json:"repo,omitempty"`
    Provider string                `json:"provider,omitempty"`
    Timeout  int                   `json:"timeout,omitempty"`
    Scope    *internal.Scope       `json:"scope,omitempty"`
    Tools    map[string]ToolConfig `json:"tools,omitempty"`
    Profiles []string              `json:"profiles,omitempty"`  // NEW
    Model    string                `json:"model,omitempty"`
    Budget   float64               `json:"budget_usd,omitempty"`
    Debug    bool                  `json:"debug,omitempty"`
}
```

Resolution priority in `DispatchTask`:

1. If `Profiles` is set, resolve and merge all profiles into a `tools` map.
2. If `Tools` is also set, merge it on top (inline tools override/extend profiles).
3. Convert the final `tools` map to a `Scope` (existing `resolveToolsToScope`).
4. If only `Scope` is set (no profiles, no tools), use it directly (backward compat).

```go
func (d *Dispatcher) resolveEffectiveTools(ctx context.Context, req TaskRequest, owner string) (map[string]ToolConfig, error) {
    var tools map[string]ToolConfig

    if len(req.Profiles) > 0 {
        // Resolve profiles
        var profiles []SecurityProfile
        for _, name := range req.Profiles {
            profile, err := d.profileStore.GetProfile(ctx, name, owner)
            if err != nil {
                return nil, fmt.Errorf("profile %q not found: %w", name, err)
            }
            profiles = append(profiles, *profile)
        }
        tools = MergeProfiles(profiles)
    }

    // Merge inline tools on top of profile-derived tools
    if req.Tools != nil {
        if tools == nil {
            tools = make(map[string]ToolConfig)
        }
        for name, cfg := range req.Tools {
            existing, ok := tools[name]
            if !ok || !cfg.Enabled {
                tools[name] = cfg
                continue
            }
            existing.Repos = unionStrings(existing.Repos, cfg.Repos)
            existing.Operations = unionStrings(existing.Operations, cfg.Operations)
            tools[name] = existing
        }
    }

    return tools, nil
}
```

### 1.8 API Endpoints

```
GET    /api/v1/profiles              -- list profiles (user's + system)
GET    /api/v1/profiles/{name}       -- get profile details
POST   /api/v1/profiles              -- create profile
PUT    /api/v1/profiles/{name}       -- update profile
DELETE /api/v1/profiles/{name}       -- delete profile (own only; cannot delete system)
POST   /api/v1/profiles/build        -- AI-assisted profile builder (Feature 2)
POST   /api/v1/profiles/preview      -- preview merged scope from profile names
```

**Create profile request:**

```json
{
    "name": "pulp-contributor",
    "display_name": "Pulp Contributor",
    "description": "Read/write access to Pulp project repos with draft PR creation",
    "tools": {
        "github": {
            "enabled": true,
            "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
            "operations": ["clone", "push_branch", "create_pr_draft", "read_prs", "create_comment"]
        }
    }
}
```

**Preview request (for the dashboard):**

```json
{
    "profiles": ["pulp-contributor", "code-reader"],
    "tools": {
        "github": {
            "enabled": true,
            "repos": ["extra/repo"],
            "operations": ["read_issues"]
        }
    }
}
```

**Preview response:**

```json
{
    "effective_tools": {
        "github": {
            "enabled": true,
            "repos": ["pulp/pulpcore", "pulp/pulp_rpm", "extra/repo", "*"],
            "operations": ["clone", "push_branch", "create_pr_draft", "read_prs", "create_comment", "read_issues", "read_contents"]
        }
    },
    "scope": {
        "services": {
            "github": {
                "repos": ["pulp/pulpcore", "pulp/pulp_rpm", "extra/repo", "*"],
                "operations": ["clone", "push_branch", "create_pr_draft", "read_prs", "create_comment", "read_issues", "read_contents"]
            }
        }
    },
    "profiles_resolved": ["pulp-contributor", "code-reader"]
}
```

### 1.9 Seeded System Profiles

Bridge seeds common profiles on startup (owner=''):

```go
var systemProfiles = []SecurityProfile{
    {
        Name:        "code-reader",
        DisplayName: "Code Reader",
        Description: "Read-only access to all repositories on all configured services",
        Tools: map[string]ToolConfig{
            "github": {Enabled: true, Repos: []string{"*"}, Operations: []string{
                "clone", "read_prs", "read_issues", "read_contents", "read_actions",
            }},
            "gitlab": {Enabled: true, Repos: []string{"*"}, Operations: []string{
                "clone", "read_mrs", "read_issues", "read_contents", "read_pipelines",
            }},
        },
    },
    {
        Name:        "contributor",
        DisplayName: "Contributor",
        Description: "Read access plus branch push and draft PR creation on all repos",
        Tools: map[string]ToolConfig{
            "github": {Enabled: true, Repos: []string{"*"}, Operations: []string{
                "clone", "read_prs", "read_issues", "read_contents", "read_actions",
                "push_branch", "create_pr_draft", "create_comment",
            }},
            "gitlab": {Enabled: true, Repos: []string{"*"}, Operations: []string{
                "clone", "read_mrs", "read_issues", "read_contents", "read_pipelines",
                "push_branch", "create_mr_draft", "create_comment",
            }},
        },
    },
    {
        Name:        "maintainer",
        DisplayName: "Maintainer",
        Description: "Full access including PR merge and branch deletion on all repos",
        Tools: map[string]ToolConfig{
            "github": {Enabled: true, Repos: []string{"*"}, Operations: []string{"*"}},
            "gitlab": {Enabled: true, Repos: []string{"*"}, Operations: []string{"*"}},
        },
    },
}
```

---

## 2. Bridge System LLM Configuration

### 2.1 Problem

Bridge needs its own LLM connection for system-level AI features, independent
of per-user task LLM credentials. Initial use cases:

1. **Profile Builder** -- natural language to security profile conversion
   ("I want to contribute to Pulp repos" -> structured profile)
2. **Schedule Name Generation** -- auto-generate human-friendly schedule names
3. **Future: AI-powered scope resolution** -- suggest profiles based on prompt

This LLM connection is system-wide (configured by admin, not per-user) and
is used by Bridge directly (not routed through Gate, since Bridge is the
trusted controller).

### 2.2 Configuration Model

The system LLM is configured via environment variables, consistent with how
Bridge loads other configuration. It can also be set in the YAML config file
or via a dedicated admin API endpoint.

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `BRIDGE_LLM_PROVIDER` | (unset) | `anthropic` or `google-vertex` |
| `BRIDGE_LLM_API_KEY` | (unset) | API key (Anthropic) or service account JSON path (Vertex) |
| `BRIDGE_LLM_MODEL` | `claude-sonnet-4-20250514` | Model to use for system LLM calls |
| `BRIDGE_LLM_CREDENTIAL` | (unset) | Name of a credential in the credential store (alternative to API key) |

**YAML config:**

```yaml
system_llm:
  provider: anthropic
  model: claude-sonnet-4-20250514
  credential: system-anthropic    # references a credential in provider_credentials
```

**Resolution order:**
1. Environment variables (highest priority)
2. YAML config file
3. Fall back to first available LLM provider credential in the credential store
4. If none, system LLM features are disabled (not an error)

### 2.3 Config Changes

**File: `internal/bridge/config.go`**

```go
type Config struct {
    // ... existing fields ...

    // System LLM configuration (for AI-powered Bridge features)
    SystemLLM SystemLLMConfig `yaml:"system_llm"`
}

type SystemLLMConfig struct {
    Provider   string `yaml:"provider"`    // "anthropic" or "google-vertex"
    Model      string `yaml:"model"`
    APIKey     string `yaml:"-"`           // from env, never in YAML
    Credential string `yaml:"credential"` // name in credential store
}
```

In `LoadConfig()`:

```go
// System LLM from env
if v := os.Getenv("BRIDGE_LLM_PROVIDER"); v != "" {
    cfg.SystemLLM.Provider = v
}
if v := os.Getenv("BRIDGE_LLM_API_KEY"); v != "" {
    cfg.SystemLLM.APIKey = v
}
if v := os.Getenv("BRIDGE_LLM_MODEL"); v != "" {
    cfg.SystemLLM.Model = v
}
if v := os.Getenv("BRIDGE_LLM_CREDENTIAL"); v != "" {
    cfg.SystemLLM.Credential = v
}
if cfg.SystemLLM.Model == "" {
    cfg.SystemLLM.Model = "claude-sonnet-4-20250514"
}
```

### 2.4 Bridge LLM Client

**New file: `internal/bridge/llm.go`**

```go
// BridgeLLM provides LLM capabilities for Bridge system features.
// It calls the LLM API directly (not through Gate).
type BridgeLLM struct {
    provider  string  // "anthropic" or "google-vertex"
    model     string
    apiKey    string
    credStore *CredentialStore
    credName  string  // credential name in store (alternative to apiKey)
}

// NewBridgeLLM creates a BridgeLLM client from config.
// Returns nil if no system LLM is configured (features will be disabled).
func NewBridgeLLM(cfg SystemLLMConfig, credStore *CredentialStore) *BridgeLLM

// Available returns true if the system LLM is configured and usable.
func (b *BridgeLLM) Available() bool

// Complete sends a prompt to the LLM and returns the text response.
// maxTokens limits output length. Returns error if system LLM is not configured.
func (b *BridgeLLM) Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int) (string, error)
```

The `Complete` method:
1. Acquires a token (from `apiKey` directly, or via `credStore.AcquireToken`)
2. Makes an HTTP POST to the Anthropic Messages API (or Vertex AI equivalent)
3. Parses the response and returns the text content
4. Handles token refresh for Vertex AI bearer tokens

This is intentionally simple -- a single synchronous call, no streaming,
no tool use. System LLM calls are short-lived and low-volume.

### 2.5 Profile Builder

**Endpoint: `POST /api/v1/profiles/build`**

Uses the system LLM to convert a natural language description into a
structured security profile.

```json
// Request
{
    "description": "I want to contribute to the Pulp project on GitHub. I should be able to read issues and PRs, push feature branches, and create draft PRs on pulpcore and pulp_rpm."
}

// Response
{
    "profile": {
        "name": "pulp-contributor",
        "display_name": "Pulp Contributor",
        "description": "Contributor access to Pulp project repositories",
        "tools": {
            "github": {
                "enabled": true,
                "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
                "operations": ["clone", "read_prs", "read_issues", "read_contents", "push_branch", "create_pr_draft", "create_comment"]
            }
        }
    }
}
```

Implementation:

```go
func (a *API) handleProfileBuild(w http.ResponseWriter, r *http.Request) {
    if a.bridgeLLM == nil || !a.bridgeLLM.Available() {
        respondError(w, http.StatusServiceUnavailable, "system LLM not configured")
        return
    }

    var req struct {
        Description string `json:"description"`
    }
    // ... decode, validate ...

    // Fetch available tools for context
    tools, _ := a.toolStore.ListTools(r.Context(), "")

    systemPrompt := buildProfileBuilderSystemPrompt(tools)
    response, err := a.bridgeLLM.Complete(r.Context(), systemPrompt, req.Description, 2048)
    // ... parse JSON from response, validate, return ...
}
```

The system prompt includes:
- The list of available tools with their operations
- The profile JSON schema
- Instructions to choose minimal permissions (principle of least privilege)
- Instructions to output valid JSON only

### 2.6 Security Considerations

**Prompt injection:**
The profile builder takes user input and sends it to the LLM. The system
prompt is structured to constrain the output to valid profile JSON. However,
Bridge MUST validate the returned profile before storing it:
- Tool names must exist in the tool registry
- Operations must be valid for the specified tool
- Repo patterns must be syntactically valid
- The profile is presented to the user for review before saving (the build
  endpoint returns a preview, not auto-saved)

**Credential exposure:**
The system LLM API key (or credential reference) is stored in Bridge's
config or the credential store. It is never exposed to Skiff, Gate, or
any API response. The `SystemLLMConfig.APIKey` field uses `yaml:"-"` to
prevent accidental serialization.

**Cost control:**
System LLM calls use a hardcoded max_tokens limit (2048 for profile
builder, 256 for name generation). Bridge logs system LLM usage for
auditing. No budget enforcement is needed since these are admin-initiated
low-volume calls.

### 2.7 Admin API for System LLM Configuration

**Endpoint: `GET /api/v1/admin/system-llm`**

Returns the current system LLM configuration (without the API key):

```json
{
    "configured": true,
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "source": "env"  // or "credential_store" or "config_file"
}
```

**Endpoint: `PUT /api/v1/admin/system-llm`**

Updates the system LLM configuration. Requires admin privileges. This stores
the credential in the credential store (not in config) and updates Bridge's
in-memory config:

```json
{
    "provider": "anthropic",
    "model": "claude-sonnet-4-20250514",
    "api_key": "sk-ant-..."
}
```

This creates/updates a credential named `__system_llm__` in the credential
store and updates `Bridge.cfg.SystemLLM` in memory. The credential name
prefix `__` marks it as a system credential.

---

## 3. GitLab Private Server Support

### 3.1 Problem

The GitLab builtin tool hardcodes `gitlab.com` as the API host. Users with
self-hosted GitLab instances (e.g., `gitlab.example.com`) cannot use the
GitLab tool. Three components need to know the GitLab host:

1. **Gate proxy** -- must forward API requests to the correct host
2. **Git credential helper** -- must recognize the host for credential injection
3. **MCP server / glab CLI** -- must point to the correct API URL

### 3.2 Design: Per-Credential Host URL

The GitLab host is stored **per-credential** in the `provider_credentials`
table, using a new `api_host` column. This is the right level because
different credentials may authenticate to different GitLab instances.

**Migration: `008_credential_api_host.sql`**

```sql
-- 008_credential_api_host.sql
-- Add api_host column to provider_credentials for self-hosted service instances.

ALTER TABLE provider_credentials
    ADD COLUMN IF NOT EXISTS api_host TEXT DEFAULT '';
```

**Credential creation (updated):**

```json
{
    "name": "my-gitlab",
    "provider": "gitlab",
    "auth_type": "pat",
    "credential": "glpat-...",
    "api_host": "gitlab.example.com"
}
```

When `api_host` is empty for a gitlab credential, it defaults to `gitlab.com`.
When `api_host` is empty for a github credential, it defaults to `api.github.com`.

### 3.3 Credential Store Changes

**File: `internal/bridge/credentials.go`**

Add `APIHost` field to `Credential`:

```go
type Credential struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Provider  string    `json:"provider"`
    AuthType  string    `json:"auth_type"`
    ProjectID string    `json:"project_id"`
    Region    string    `json:"region"`
    APIHost   string    `json:"api_host,omitempty"`  // NEW
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
    Owner     string    `json:"owner,omitempty"`
}
```

Update `CreateCredential`, `ListCredentials`, `GetCredential` to include
the `api_host` column.

Add a new method:

```go
// AcquireSCMTokenWithHost looks up an SCM credential and returns both the
// token and the API host. Falls back to default hosts when api_host is empty.
func (cs *CredentialStore) AcquireSCMTokenWithHost(ctx context.Context, service string) (token string, apiHost string, err error) {
    var encrypted []byte
    var host *string
    err = cs.db.QueryRow(ctx,
        `SELECT credential, api_host FROM provider_credentials
         WHERE provider = $1 OR name = $1
         ORDER BY created_at DESC LIMIT 1`,
        service).Scan(&encrypted, &host)
    if err != nil {
        return "", "", fmt.Errorf("no credential found for service %q: %w", service, err)
    }
    raw, err := decrypt(cs.key, encrypted)
    if err != nil {
        return "", "", fmt.Errorf("decrypting credential for %q: %w", service, err)
    }
    apiHost = ""
    if host != nil {
        apiHost = *host
    }
    return string(raw), apiHost, nil
}
```

### 3.4 Dispatcher Changes

**File: `internal/bridge/dispatcher.go`**

When resolving SCM credentials, use `AcquireSCMTokenWithHost` instead of
`AcquireSCMToken`. The returned `apiHost` is used to:

1. Override the `api_host` in the tool's `ToolConfig` for Gate
2. Set the correct `GITLAB_API_URL` and `GLAB_HOST` env vars for Skiff
3. Set the correct `GATE_GITLAB_HOST` env var for Gate

```go
// In the credential resolution loop:
realToken, apiHost, err := d.credStore.AcquireSCMTokenWithHost(ctx, toolName)
if err != nil {
    log.Printf("warning: no credential for %s: %v", toolName, err)
    continue
}

// If credential has a custom API host, override the tool config
if apiHost != "" && toolName == "gitlab" {
    toolCfg := toolConfigs[toolName]
    toolCfg.APIHost = apiHost
    toolConfigs[toolName] = toolCfg
}
```

For Skiff env vars:

```go
if token, ok := scmDummyTokens["gitlab"]; ok {
    gitlabHost := "gitlab.com"
    if tc, ok := toolConfigs["gitlab"]; ok && tc.APIHost != "" {
        gitlabHost = tc.APIHost
    }
    skiffEnv["GITLAB_TOKEN"] = token
    skiffEnv["GITLAB_API_URL"] = fmt.Sprintf("http://%s:8443/gitlab/api/v4", gateName)
    skiffEnv["GLAB_HOST"] = fmt.Sprintf("http://%s:8443/gitlab", gateName)
}
```

For Gate env vars:

```go
if tc, ok := toolConfigs["gitlab"]; ok && tc.APIHost != "" {
    gateEnv["GATE_GITLAB_HOST"] = tc.APIHost
}
```

### 3.5 Gate Changes

Gate already has `GitLabHost` in its config and uses it as a fallback in
`handleSCMProxy`. The `ToolConfigs` mechanism already supports custom
`api_host` values. The main changes needed:

1. **`isServiceHost`** -- must recognize custom GitLab hosts:

```go
func (p *Proxy) isRegisteredServiceHost(hostname string) bool {
    for _, tc := range p.config.ToolConfigs {
        if hostname == tc.APIHost || strings.HasSuffix(hostname, "."+tc.APIHost) {
            return true
        }
    }
    return isServiceHost(hostname) // fallback to hardcoded check
}
```

2. **`identifyService`** -- must map custom hosts to tool names:

```go
func (p *Proxy) identifyServiceFromHost(hostname string) string {
    for name, tc := range p.config.ToolConfigs {
        if hostname == tc.APIHost || strings.HasSuffix(hostname, "."+tc.APIHost) {
            return name
        }
    }
    return identifyService(hostname)
}
```

3. **`HandleGitCredential`** -- must recognize custom GitLab hosts:

```go
// In HandleGitCredential, replace the hardcoded switch with:
var service string
for toolName, tc := range toolConfigs {
    gitHost := tc.APIHost
    // Strip "api." prefix for GitHub
    gitHost = strings.TrimPrefix(gitHost, "api.")
    if gitHost != "" && strings.Contains(host, gitHost) {
        service = toolName
        break
    }
}
// Fallback to hardcoded detection
if service == "" {
    switch {
    case strings.Contains(host, "github.com"):
        service = "github"
    case strings.Contains(host, "gitlab"):
        service = "gitlab"
    }
}
```

### 3.6 Security Profile Interaction

When a security profile references `gitlab` with repos like `["myorg/project"]`,
the credential's `api_host` determines which GitLab instance those repos
refer to. This is resolved at dispatch time, not at profile creation time.
A single profile can work with different GitLab instances depending on which
credential is configured.

---

## 4. How TaskRequest Changes (Summary)

### Before (current)

```go
type TaskRequest struct {
    Prompt   string                `json:"prompt"`
    Repo     string                `json:"repo,omitempty"`
    Provider string                `json:"provider,omitempty"`
    Timeout  int                   `json:"timeout,omitempty"`
    Scope    *internal.Scope       `json:"scope,omitempty"`
    Tools    map[string]ToolConfig `json:"tools,omitempty"`
    Model    string                `json:"model,omitempty"`
    Budget   float64               `json:"budget_usd,omitempty"`
    Debug    bool                  `json:"debug,omitempty"`
}
```

### After

```go
type TaskRequest struct {
    Prompt   string                `json:"prompt"`
    Repo     string                `json:"repo,omitempty"`
    Provider string                `json:"provider,omitempty"`
    Timeout  int                   `json:"timeout,omitempty"`
    Scope    *internal.Scope       `json:"scope,omitempty"`
    Tools    map[string]ToolConfig `json:"tools,omitempty"`
    Profiles []string              `json:"profiles,omitempty"`   // NEW
    Model    string                `json:"model,omitempty"`
    Budget   float64               `json:"budget_usd,omitempty"`
    Debug    bool                  `json:"debug,omitempty"`
}
```

### Resolution Order

```
Profiles   →  merge into tools map
                    ↓
Inline Tools → merge on top (union)
                    ↓
              resolveToolsToScope()
                    ↓
Scope (if no profiles/tools)  →  used directly
                    ↓
              Final scope for Gate
```

All three input modes (`profiles`, `tools`, `scope`) produce the same output:
a `Scope` struct that is serialized to `GATE_SCOPE` for the Gate sidecar.

---

## 5. Migration and Backward Compatibility

### 5.1 Database Migrations

Two new migration files:

| File | Contents |
|------|----------|
| `007_security_profiles.sql` | `security_profiles` table + indexes |
| `008_credential_api_host.sql` | `api_host` column on `provider_credentials` |

These are additive (new table, new nullable column). No existing data is
modified.

### 5.2 API Backward Compatibility

All changes are additive:

1. **TaskRequest**: `profiles` is optional. Omitting it preserves existing
   behavior. Existing clients that send `tools` or `scope` continue to work
   unchanged.

2. **Credential API**: `api_host` is optional in create/update requests.
   Omitting it defaults to the standard host (github.com/gitlab.com).
   Existing credentials continue to work.

3. **New endpoints** (`/api/v1/profiles/*`, `/api/v1/profiles/build`,
   `/api/v1/admin/system-llm`) do not conflict with existing routes.

4. **Gate**: `ToolConfigs` with custom `api_host` values is already supported.
   The only change is that Bridge now populates `api_host` from the credential
   rather than only from the tool definition.

### 5.3 System Profile Seeding

System profiles are seeded on startup using INSERT ... ON CONFLICT DO UPDATE,
identical to how builtin tools are seeded. Existing system profiles are
updated; user-created profiles are never modified.

---

## 6. File-by-File Implementation Plan

### Phase A: Security Profiles (Core)

| # | File | Change |
|---|------|--------|
| A1 | `internal/bridge/migrations/007_security_profiles.sql` | New table |
| A2 | `internal/bridge/profiles.go` | `SecurityProfile`, `ProfileStore` (CRUD + merge) |
| A3 | `internal/bridge/profiles_test.go` | Unit tests for CRUD and merge logic |
| A4 | `internal/bridge/api.go` | Register profile routes, handlers |
| A5 | `internal/bridge/api.go` | Profile preview endpoint |
| A6 | `internal/bridge/dispatcher.go` | Add `Profiles` field, `resolveEffectiveTools` |
| A7 | `internal/bridge/dispatcher.go` | Wire `profileStore` into Dispatcher |
| A8 | `cmd/bridge/main.go` | Create ProfileStore, pass to API + Dispatcher, seed system profiles |

**Dependencies:** A1 before A2. A2 before A3-A8. A4 and A6 can parallel.

### Phase B: Bridge System LLM

| # | File | Change |
|---|------|--------|
| B1 | `internal/bridge/config.go` | Add `SystemLLMConfig`, env var loading |
| B2 | `internal/bridge/llm.go` | `BridgeLLM` client (Complete method) |
| B3 | `internal/bridge/llm_test.go` | Unit tests (mock HTTP) |
| B4 | `internal/bridge/api.go` | Profile builder endpoint (`/api/v1/profiles/build`) |
| B5 | `internal/bridge/api.go` | Admin system LLM config endpoint |
| B6 | `cmd/bridge/main.go` | Create BridgeLLM, pass to API |

**Dependencies:** B1 before B2. B2 before B3-B6. B4 depends on A4.

### Phase C: GitLab Private Server

| # | File | Change |
|---|------|--------|
| C1 | `internal/bridge/migrations/008_credential_api_host.sql` | Add column |
| C2 | `internal/bridge/credentials.go` | Add `APIHost` field, update CRUD, add `AcquireSCMTokenWithHost` |
| C3 | `internal/bridge/credentials_test.go` | Tests for api_host handling |
| C4 | `internal/bridge/dispatcher.go` | Use `AcquireSCMTokenWithHost`, override tool config |
| C5 | `internal/gate/proxy.go` | `isRegisteredServiceHost`, `identifyServiceFromHost` |
| C6 | `internal/gate/scope.go` | Update `HandleGitCredential` to use tool configs |
| C7 | `internal/gate/scope_test.go` | Tests for custom GitLab host handling |

**Dependencies:** C1 before C2. C2 before C3-C4. C5 and C6 can parallel.

### Phase D: Dashboard + Integration

| # | File | Change |
|---|------|--------|
| D1 | `web/js/app.js` | Profile management UI (list, create, edit, delete) |
| D2 | `web/js/app.js` | Profile selector in task creation form |
| D3 | `web/js/app.js` | Profile builder UI (natural language input) |
| D4 | `web/js/app.js` | System LLM config page (admin) |
| D5 | `web/js/app.js` | Credential form: add api_host field for GitLab |

### Dependency Graph

```
Phase A (Profiles Core)
    |
    +---> Phase B (System LLM)  -- B4 depends on A4
    |
    +---> Phase D (Dashboard)   -- D1-D3 depend on A4

Phase C (GitLab Private) -- independent of A and B

Phase D:
    D1-D3 depend on Phase A
    D3 depends on Phase B
    D4 depends on Phase B
    D5 depends on Phase C
```

---

## 7. Open Questions

1. **Profile sharing**: Should profiles be shareable between users? Current
   design: profiles are per-user (isolated) plus system profiles (visible to
   all). Team-shared profiles would require a team/org model that does not
   exist yet.

2. **Profile versioning**: Should profile updates be versioned? A task
   records the profile names it used but not the profile contents at dispatch
   time. If a profile is modified after a task runs, the audit trail is
   incomplete. Consider snapshotting the resolved scope in the session record
   (which already happens via the `scope` column).

3. **Profile inheritance**: Should profiles be able to extend other profiles?
   ("infra-deployer extends contributor"). Current design: no inheritance;
   stacking at task submission time achieves the same result without the
   complexity of dependency chains.

4. **System LLM rate limiting**: Should Bridge rate-limit system LLM calls?
   The profile builder is user-facing and could be abused. Consider a simple
   per-user rate limit (e.g., 10 calls/minute).

5. **GitHub Enterprise Server**: The same per-credential `api_host` pattern
   used for GitLab private servers should work for GitHub Enterprise Server.
   The implementation is symmetric but needs testing with GHES API endpoints
   (which use `/api/v3/` prefix instead of root).
