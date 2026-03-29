# Gate SCM Authorization Design

## Status: Implemented (Phase 1-3)

Phases 1-3 (core SCM proxy, git transport, CLI tools) are implemented.
Deferred: Phase 4 (draft PR enforcement), Phase 5 (dashboard scope builder),
Phase 6 (alias expansion).

This document describes how Gate handles GitHub and GitLab operations with
operation-level authorization, credential injection, and dummy-token isolation.

---

## 1. Scope Type Changes

### Current types (`internal/types.go`)

```go
type Scope struct {
    Services map[string]ServiceScope `json:"services"`
}

type ServiceScope struct {
    Repos      []string `json:"repos,omitempty"`
    Operations []string `json:"operations"`
}
```

These types are already sufficient for SCM authorization. No structural changes
are needed. The `Services` map uses keys like `"github"` and `"gitlab"`, the
`Repos` list specifies allowed repositories (with wildcard support via `"*"` and
`"org/*"`), and `Operations` lists the allowed operation names.

### Operation taxonomy

The following operation names are recognized by Gate's scope checker. They are
grouped into tiers to simplify common configurations.

**Tier 1 -- Read-only (low risk):**

| Operation        | GitHub API path pattern                       | GitLab API path pattern                |
|------------------|-----------------------------------------------|----------------------------------------|
| `read`           | `GET /repos/{o}/{r}` (catch-all)              | `GET /api/v4/projects/{id}` (catch-all)|
| `read_prs`       | `GET /repos/{o}/{r}/pulls/**`                 | `GET .../merge_requests/**`            |
| `read_issues`    | `GET /repos/{o}/{r}/issues/**`                | `GET .../issues/**`                    |
| `read_contents`  | `GET /repos/{o}/{r}/contents/**`              | `GET .../repository/**`                |
| `read_commits`   | `GET /repos/{o}/{r}/commits/**`               | `GET .../repository/commits/**`        |
| `read_branches`  | `GET /repos/{o}/{r}/branches/**`              | `GET .../repository/branches/**`       |
| `read_actions`   | `GET /repos/{o}/{r}/actions/**`               | `GET .../pipelines/**`                 |
| `read_releases`  | `GET /repos/{o}/{r}/releases/**`              | `GET .../releases/**`                  |
| `read_git`       | `GET /repos/{o}/{r}/git/**`                   | (n/a via API)                          |

**Tier 2 -- Write, scoped (medium risk):**

| Operation        | GitHub API path pattern                       | GitLab API path pattern                |
|------------------|-----------------------------------------------|----------------------------------------|
| `create_pr_draft`| `POST /repos/{o}/{r}/pulls` (body: draft=true)| `POST .../merge_requests` (wip prefix) |
| `create_pr`      | `POST /repos/{o}/{r}/pulls`                   | `POST .../merge_requests`              |
| `update_pr`      | `PATCH /repos/{o}/{r}/pulls/{n}`              | `PUT .../merge_requests/{n}`           |
| `create_issue`   | `POST /repos/{o}/{r}/issues`                  | `POST .../issues`                      |
| `update_issue`   | `PATCH /repos/{o}/{r}/issues/{n}`             | `PUT .../issues/{n}`                   |
| `create_comment` | `POST /repos/{o}/{r}/issues/{n}/comments`     | `POST .../notes`                       |
| `create_review`  | `POST /repos/{o}/{r}/pulls/{n}/reviews`       | `POST .../merge_requests/{n}/approve`  |
| `create_branch`  | `POST /repos/{o}/{r}/git/refs`                | `POST .../repository/branches`         |
| `write_contents` | `PUT /repos/{o}/{r}/contents/**`              | `PUT .../repository/files/**`          |
| `write_git`      | `POST /repos/{o}/{r}/git/**`                  | (n/a via API)                          |

**Tier 3 -- Dangerous (high risk):**

| Operation        | GitHub API path pattern                       | GitLab API path pattern                |
|------------------|-----------------------------------------------|----------------------------------------|
| `merge_pr`       | `PUT /repos/{o}/{r}/pulls/{n}/merge`          | `PUT .../merge_requests/{n}/merge`     |
| `delete_branch`  | `DELETE /repos/{o}/{r}/git/refs/**`           | `DELETE .../repository/branches/**`    |
| `write_actions`  | `POST /repos/{o}/{r}/actions/**`              | `POST .../pipelines/**`                |
| `write_releases` | `POST /repos/{o}/{r}/releases`                | `POST .../releases`                    |
| `write`          | catch-all for any non-GET request             | catch-all for any non-GET request      |

**Git transport operations** (enforced at the credential helper level):

| Operation        | Trigger                                                  |
|------------------|----------------------------------------------------------|
| `clone`          | `git clone` -- credential helper returns creds for read  |
| `fetch`          | `git fetch` -- credential helper returns creds for read  |
| `push_branch`    | `git push` to non-default branch                        |
| `push_main`      | `git push` to default/protected branch                  |

**Convenience aliases** (expanded by Bridge before passing to Gate):

| Alias            | Expands to                                                        |
|------------------|-------------------------------------------------------------------|
| `read_all`       | all `read_*` operations + `clone` + `fetch`                       |
| `contribute`     | `read_all` + `push_branch` + `create_pr_draft` + `create_comment`|
| `maintain`       | `contribute` + `create_pr` + `merge_pr` + `create_branch`        |
| `*`              | all operations (already supported)                                |

### Example scope JSON

```json
{
  "services": {
    "github": {
      "repos": ["pulp/pulpcore", "pulp/pulp_rpm"],
      "operations": ["clone", "push_branch", "create_pr_draft", "read_all"]
    },
    "gitlab": {
      "repos": ["myorg/*"],
      "operations": ["clone", "read_all", "create_mr_draft", "create_comment"]
    }
  }
}
```

---

## 2. Gate Proxy Changes

### 2.1 API proxy endpoints (new)

Gate adds two new reverse-proxy route prefixes to its mux. These handle GitHub
and GitLab API calls when the Skiff container is configured to use Gate as the
API base URL (see Section 4).

```
/github/   -->  https://api.github.com/
/gitlab/   -->  https://gitlab.com/    (or self-hosted, from GATE_GITLAB_HOST)
```

These work identically to the existing `/v1/` LLM proxy pattern:

1. Skiff sets `GITHUB_API_URL=http://gate-<taskID>:8443/github` (plain HTTP).
2. The `gh` CLI, GitHub MCP server, and any HTTP client send requests to Gate
   over plain HTTP.
3. Gate receives the request, sees the `/github/` prefix, and:
   a. Strips the prefix to get the real API path.
   b. Calls `CheckAccess(method, path, scope)` to classify the operation.
   c. If denied, returns 403 with the reason.
   d. If allowed, injects real credentials and reverse-proxies to the real API
      over HTTPS.

**File: `internal/gate/proxy.go`**

New handler registrations in `Handler()`:

```go
mux.HandleFunc("/github/", func(w http.ResponseWriter, r *http.Request) {
    p.handleSCMProxy(w, r, "github")
})

mux.HandleFunc("/gitlab/", func(w http.ResponseWriter, r *http.Request) {
    p.handleSCMProxy(w, r, "gitlab")
})
```

New method:

```go
func (p *Proxy) handleSCMProxy(w http.ResponseWriter, r *http.Request, service string) {
    // 1. Strip the /<service>/ prefix to get the real API path
    prefix := "/" + service + "/"
    apiPath := strings.TrimPrefix(r.URL.Path, prefix)
    if apiPath == "" {
        apiPath = "/"
    }

    // 2. Reconstruct the full URL as if it were the real API
    var targetHost string
    switch service {
    case "github":
        targetHost = "api.github.com"
    case "gitlab":
        targetHost = p.config.GitLabHost // defaults to "gitlab.com"
        if targetHost == "" {
            targetHost = "gitlab.com"
        }
    }

    fakeURL := fmt.Sprintf("https://%s/%s", targetHost, apiPath)
    if r.URL.RawQuery != "" {
        fakeURL += "?" + r.URL.RawQuery
    }

    // 3. Check access against scope
    result := CheckAccess(r.Method, fakeURL, p.config.Scope)
    if !result.Allowed {
        http.Error(w, "Forbidden: "+result.Reason, http.StatusForbidden)
        p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "deny", http.StatusForbidden)
        return
    }

    // 4. Build real target URL
    targetURL, _ := url.Parse(fmt.Sprintf("https://%s/%s", targetHost, apiPath))
    if r.URL.RawQuery != "" {
        targetURL.RawQuery = r.URL.RawQuery
    }

    // 5. Reverse proxy with credential injection
    proxy := &httputil.ReverseProxy{
        Director: func(req *http.Request) {
            req.URL = targetURL
            req.Host = targetHost
            p.injectServiceCredential(req, service)
            // Strip the dummy token that the client sent
            req.Header.Del("X-Session-Token")
        },
    }
    proxy.ServeHTTP(w, r)
    p.logEntry(r.Method, fakeURL, result.Service, result.Operation, "allow", http.StatusOK)
}
```

### 2.2 Dummy token validation

> **Note: Token validation was deferred.** The implementation relies on network
> isolation (internal network between Skiff and Gate) as the primary security
> boundary. Gate does not currently validate the dummy token in
> `handleSCMProxy` -- it accepts any request arriving on its internal endpoint.
> This is acceptable because Skiff can only reach Gate via the internal podman
> network or Kubernetes NetworkPolicy; no external traffic can reach Gate's
> listening port.

The dummy token is a random UUID generated per session. It has no value outside
the Gate sidecar -- it is not a GitHub token, not a PAT, not anything usable
against any real API. A future enhancement could add token validation as
defense-in-depth.

### 2.3 Git credential helper changes

The existing `/git-credential` endpoint already works well. Two enhancements:

**a. Operation-level enforcement for git operations:**

Currently, `HandleGitCredential` checks repo access but not the specific git
operation (clone vs push). The credential helper protocol does not convey the
operation type. Instead, we enforce this at the *transport* level:

- For `clone`/`fetch`: git sends credential requests with the repo URL. Gate
  checks that `clone` or `fetch` is in the allowed operations. Since both are
  read-only, they can share a single `clone` permission (fetch is always
  implied by clone).
- For `push`: git sends credential requests before push. Gate cannot distinguish
  push from clone at the credential-helper level because both use the same
  credential request format.

**Solution for push enforcement:** Gate issues a read-only token scope when
only `clone`/`fetch` operations are authorized. When `push_branch` or
`push_main` are authorized, Gate issues a token with write access. This is
accomplished by:

1. If the real credential is a GitHub PAT with full repo scope, Gate always
   returns it (the PAT itself cannot be downscoped at the credential-helper
   level). Push enforcement then relies on a pre-receive hook or on the
   API-level scope check.
2. If the real credential is a GitHub App installation token, Bridge can
   request a token with `contents:read` only (for clone) or `contents:write`
   (for push). Gate stores both variants.
3. For GitLab, project access tokens can be scoped to `read_repository` or
   `write_repository`.

In Phase 1, Gate returns the full credential for any repo in scope and relies
on the scope check being correct. This is acceptable because:
- Skiff never sees the real credential.
- The real credential is only returned to git (via the credential helper), and
  git only uses it for the immediate operation.
- The audit log records all credential dispensations.

**b. Enhanced logging:**

```go
p.logEntry("POST", fmt.Sprintf("git-credential://%s/%s", host, repoPath),
    service, "git_credential", "allow", http.StatusOK)
```

### 2.4 CONNECT tunnel changes

The implementation blocks CONNECT tunnels only to `api.github.com`. CONNECT
tunnels to `github.com` and `gitlab.com` (used for git transport) are still
allowed through the existing `tunnelToService` method, which checks that the
service is in scope.

In `handleConnect`, `api.github.com` is handled as a special case before
`isServiceHost`:

```go
case hostname == "api.github.com":
    // Block CONNECT tunnels to api.github.com -- API operations must go
    // through the /github/ proxy endpoint for operation-level enforcement.
    http.Error(w, "Forbidden: use /github/ proxy endpoint for API calls", http.StatusForbidden)
    p.logEntry("CONNECT", host, "github", "", "deny", http.StatusForbidden)
```

All other service hosts (including `github.com`, `gitlab.com`) pass through
`tunnelToService`, which validates that the service is in scope and then
creates a direct TCP tunnel. Git transport uses the credential helper
(`credential.helper`) to obtain credentials from Gate's `/git-credential`
endpoint, so the tunnel itself carries no Gate-managed secrets.

### 2.5 Config changes

**File: `internal/gate/proxy.go`**

Add to `Config`:

```go
type Config struct {
    // ... existing fields ...
    GitLabHost string // self-hosted GitLab hostname (default: "gitlab.com")
}
```

**File: `cmd/gate/main.go`**

Add env var:

```go
gitlabHost := os.Getenv("GATE_GITLAB_HOST")
if gitlabHost == "" {
    gitlabHost = "gitlab.com"
}
```

---

## 3. Bridge Credential Storage

### 3.1 Database schema changes

The existing `provider_credentials` table stores LLM provider credentials. SCM
credentials need a separate table (or the same table with a different
`provider` value) because they have different semantics:

- LLM credentials are per-provider (one Anthropic key, one Vertex SA).
- SCM credentials are per-service-instance (one GitHub PAT, one GitLab token
  for `gitlab.com`, another for `gitlab.internal.company.com`).

**Option A (recommended): Reuse `provider_credentials` with service types.**

The `provider` column already distinguishes credential types. Add new provider
values:

- `"github"` -- GitHub PAT or GitHub App credentials
- `"gitlab"` -- GitLab PAT or project access token
- `"gitlab-selfhosted"` -- self-hosted GitLab instance

The `auth_type` column gets new values:

- `"pat"` -- Personal Access Token (GitHub or GitLab)
- `"app"` -- GitHub App (private key + app ID + installation ID)
- `"project_token"` -- GitLab project access token
- `"oauth"` -- OAuth token (future)

The `credential` column (encrypted) stores the raw token or JSON-encoded
app credentials.

No schema migration is needed -- the existing table structure works.

### 3.2 Credential acquisition for SCM

Method on `CredentialStore` (`internal/bridge/credentials.go`):

```go
// AcquireSCMToken looks up a stored credential for a GitHub or GitLab service.
// Unlike LLM tokens, SCM tokens are typically PATs that don't need OAuth2 exchange.
func (cs *CredentialStore) AcquireSCMToken(ctx context.Context, service string) (string, error) {
    var encrypted []byte
    err := cs.db.QueryRow(ctx,
        `SELECT credential FROM provider_credentials WHERE provider = $1 OR name = $1 ORDER BY created_at DESC LIMIT 1`,
        service).Scan(&encrypted)
    if err != nil {
        return "", fmt.Errorf("no credential found for service %q: %w", service, err)
    }
    raw, err := decrypt(cs.key, encrypted)
    if err != nil {
        return "", fmt.Errorf("decrypting credential for %q: %w", service, err)
    }
    return string(raw), nil
}
```

Key differences from the original design:
- Queries by `provider = $1 OR name = $1` so credentials can be looked up by
  either the provider type or a user-assigned name.
- Reads only the `credential` column (not `auth_type`) -- the raw decrypted
  string is returned directly without auth_type switching. This simplifies the
  implementation since PATs and project tokens are both opaque strings.

### 3.3 Dispatcher changes

**File: `internal/bridge/dispatcher.go`**

In `DispatchTask`, after building `gateEnv`, SCM credential resolution sets
the following environment variables:

```go
// Resolve SCM credentials for services in scope.
scmCredentials := make(map[string]string)
scmDummyTokens := make(map[string]string)
for service := range scope.Services {
    if service == "github" || service == "gitlab" {
        realToken, err := d.credStore.AcquireSCMToken(ctx, service)
        if err != nil {
            log.Printf("warning: no credential for %s: %v", service, err)
            continue
        }
        scmCredentials[service] = realToken
        dummyToken := "alcove-session-" + uuid.New().String()
        scmDummyTokens[service] = dummyToken
    }
}

// Replace the empty GATE_CREDENTIALS with actual service credentials.
if len(scmCredentials) > 0 {
    credJSON, _ := json.Marshal(scmCredentials)
    gateEnv["GATE_CREDENTIALS"] = string(credJSON)
}

// Set SCM environment for Skiff tools (dummy tokens + Gate proxy URLs).
if token, ok := scmDummyTokens["github"]; ok {
    skiffEnv["GITHUB_TOKEN"] = token
    skiffEnv["GH_TOKEN"] = token
    skiffEnv["GITHUB_PERSONAL_ACCESS_TOKEN"] = token
    skiffEnv["GITHUB_API_URL"] = fmt.Sprintf("http://%s:8443/github", gateName)
    skiffEnv["GH_HOST"] = fmt.Sprintf("%s:8443", gateName)
    skiffEnv["GH_PROMPT_DISABLED"] = "1"
    skiffEnv["GH_NO_UPDATE_NOTIFIER"] = "1"
}
if token, ok := scmDummyTokens["gitlab"]; ok {
    skiffEnv["GITLAB_TOKEN"] = token
    skiffEnv["GITLAB_PERSONAL_ACCESS_TOKEN"] = token
    skiffEnv["GITLAB_API_URL"] = fmt.Sprintf("http://%s:8443/gitlab/api/v4", gateName)
    skiffEnv["GLAB_HOST"] = fmt.Sprintf("http://%s:8443/gitlab", gateName)
}
```

Notable differences from the original design:
- `GH_PROTOCOL` is not set (not needed; `GH_HOST` includes the port).
- `GL_TOKEN` and `CI_API_V4_URL` are not set.
- `GITHUB_PERSONAL_ACCESS_TOKEN`, `GH_PROMPT_DISABLED`, and
  `GH_NO_UPDATE_NOTIFIER` are set for GitHub.
- `GITLAB_PERSONAL_ACCESS_TOKEN` and `GLAB_HOST` are set for GitLab.
- `GITLAB_API_URL` points to `/gitlab/api/v4` (not `/gitlab`).

### 3.4 Dummy token properties

The dummy tokens have the following properties:

- **Format:** `alcove-session-<uuid>` -- obviously not a real PAT (GitHub PATs
  start with `ghp_`, GitLab PATs start with `glpat-`).
- **Lifetime:** exists only for the duration of the Skiff pod.
- **Scope:** meaningless -- it is just a string that Gate recognizes.
- **Usability:** if leaked or extracted from the Skiff container, the token
  cannot be used against any real API. It is not a GitHub token, not a GitLab
  token, not anything.
- **Per-service:** each service gets its own dummy token so Gate can validate
  which service the request is intended for.

---

## 4. Skiff Environment

### 4.1 Environment variables set by Bridge and skiff-init

For a task with GitHub and GitLab in scope, Skiff receives:

```bash
# Git configuration (set by skiff-init setupEnv)
GIT_TERMINAL_PROMPT=0
GATE_CREDENTIAL_URL=http://gate-<taskID>:8443   # derived from ANTHROPIC_BASE_URL
GIT_SSH_COMMAND="echo 'SSH disabled — use HTTPS' && exit 1"

# GitHub -- dummy token + API routed through Gate (set by Bridge dispatcher)
GITHUB_TOKEN=alcove-session-<uuid>
GH_TOKEN=alcove-session-<uuid>
GITHUB_PERSONAL_ACCESS_TOKEN=alcove-session-<uuid>
GITHUB_API_URL=http://gate-<taskID>:8443/github
GH_HOST=gate-<taskID>:8443
GH_PROMPT_DISABLED=1
GH_NO_UPDATE_NOTIFIER=1

# GitLab -- dummy token + API routed through Gate (set by Bridge dispatcher)
GITLAB_TOKEN=alcove-session-<uuid>
GITLAB_PERSONAL_ACCESS_TOKEN=alcove-session-<uuid>
GITLAB_API_URL=http://gate-<taskID>:8443/gitlab/api/v4
GLAB_HOST=http://gate-<taskID>:8443/gitlab

# LLM (existing)
ANTHROPIC_BASE_URL=http://gate-<taskID>:8443
ANTHROPIC_API_KEY=sk-placeholder-routed-through-gate

# Proxy (existing)
HTTP_PROXY=http://gate-<taskID>:8443
HTTPS_PROXY=http://gate-<taskID>:8443
NO_PROXY=localhost,127.0.0.1,gate-<taskID>
```

`GATE_CREDENTIAL_URL` is set by `skiff-init`'s `setupEnv()` function, derived
from `ANTHROPIC_BASE_URL` (which already points to the Gate sidecar).
`GIT_SSH_COMMAND` blocks SSH git transport to force HTTPS through Gate's
credential helper.

### 4.2 Git credential helper script

**File: `build/alcove-credential-helper` (installed to `/usr/local/bin/alcove-credential-helper`)**

This is a bash script implementing the git credential helper protocol. It
forwards credential requests to Gate's `/git-credential` HTTP endpoint:

```bash
#!/bin/bash
# alcove-credential-helper — git credential helper that delegates to Gate.
#
# Usage in git config:
#   git config --global credential.helper '/usr/local/bin/alcove-credential-helper'
#
# Requires:
#   GATE_CREDENTIAL_URL — URL of Gate's credential endpoint (e.g., http://gate-<taskID>:8443)

GATE_URL="${GATE_CREDENTIAL_URL:-http://localhost:8443}"

case "$1" in
  get)
    # Read stdin (protocol=https\nhost=github.com\n...) and POST to Gate
    input=$(cat)
    curl -s -X POST "${GATE_URL}/git-credential" \
      -H "Content-Type: text/plain" \
      -d "$input" 2>/dev/null
    ;;
  store|erase)
    # No-op: Gate manages credentials centrally
    cat > /dev/null
    ;;
  *)
    # Unknown operation, ignore
    ;;
esac
```

The credential helper reads `GATE_CREDENTIAL_URL` (set by `skiff-init` from
`ANTHROPIC_BASE_URL`) and appends `/git-credential` to form the full endpoint
URL. Git is configured system-wide to use this helper via
`git config --system credential.helper` in the Containerfile.

### 4.3 MCP server configuration

Claude Code's MCP servers (GitHub MCP, GitLab MCP) read their configuration
from environment variables. The key insight is that these MCP servers use the
same environment variables we set:

- **GitHub MCP server:** reads `GITHUB_TOKEN` and `GITHUB_API_URL`. Since we
  set `GITHUB_API_URL` to Gate's endpoint, the MCP server sends all API calls
  through Gate. The dummy `GITHUB_TOKEN` is passed in the Authorization header,
  and Gate replaces it with the real token.

- **GitLab MCP server:** reads `GITLAB_TOKEN` and `GITLAB_API_URL`. Same
  pattern.

No special MCP configuration is needed beyond the environment variables.

---

## 5. Containerfile Changes

### 5.1 Skiff base image additions

**File: `build/Containerfile.skiff-base`**

The Containerfile installs `gh`, `glab`, and the credential helper:

```dockerfile
# Install GitHub CLI (gh)
RUN dnf install -y 'dnf-command(config-manager)' && \
    dnf config-manager --add-repo https://cli.github.com/packages/rpm/gh-cli.repo && \
    dnf install -y gh --repo gh-cli && \
    dnf clean all

# Install GitLab CLI (glab) -- direct binary download (not tarball)
RUN curl -sL "https://gitlab.com/gitlab-org/cli/-/releases/permalink/latest/downloads/glab_linux_amd64" \
    -o /usr/local/bin/glab && \
    chmod +x /usr/local/bin/glab

# Install the git credential helper for Gate integration
COPY build/alcove-credential-helper /usr/local/bin/alcove-credential-helper
RUN chmod +x /usr/local/bin/alcove-credential-helper

# Configure git for Gate-proxied credential management
RUN git config --system credential.helper '/usr/local/bin/alcove-credential-helper' && \
    git config --system credential.useHttpPath true && \
    git config --system url."https://github.com/".insteadOf "git@github.com:" && \
    git config --system url."https://github.com/".insteadOf "ssh://git@github.com/" && \
    git config --system url."https://gitlab.com/".insteadOf "git@gitlab.com:" && \
    git config --system url."https://gitlab.com/".insteadOf "ssh://git@gitlab.com/"
```

Notable differences from the original design:
- `glab` is installed as a direct binary download (not a tarball extraction).
  Uses the permalink/latest URL instead of a pinned version.
- The credential helper is installed as `/usr/local/bin/alcove-credential-helper`
  (not `git-credential-alcove`). The git config references the full path.
- `credential.useHttpPath` is enabled for path-level credential scoping.
- SSH-to-HTTPS URL rewrites are configured system-wide to ensure all git
  operations go through the credential helper.

### 5.2 Gate image -- no changes needed

Gate is a static Go binary. All new functionality (SCM proxy endpoints, scope
checking) is compiled into the binary. No additional packages are needed.

---

## 6. Dashboard / API

### 6.1 Credential management API

The existing `/api/v1/credentials` endpoint already supports creating and
listing credentials. To support SCM credentials, the same endpoint is used
with new provider/auth_type values:

**Create a GitHub PAT credential:**

```http
POST /api/v1/credentials
Content-Type: application/json

{
    "name": "github-prod",
    "provider": "github",
    "auth_type": "pat",
    "credential": "ghp_xxxxxxxxxxxxxxxxxxxx"
}
```

**Create a GitLab PAT credential:**

```http
POST /api/v1/credentials
Content-Type: application/json

{
    "name": "gitlab-prod",
    "provider": "gitlab",
    "auth_type": "pat",
    "credential": "glpat-xxxxxxxxxxxxxxxxxxxx"
}
```

### 6.2 Task submission with scope

The existing `POST /api/v1/tasks` endpoint accepts a `scope` field. No API
changes are needed:

```http
POST /api/v1/tasks
Content-Type: application/json

{
    "prompt": "Fix the flaky test in test_models.py",
    "repo": "https://github.com/pulp/pulpcore.git",
    "scope": {
        "services": {
            "github": {
                "repos": ["pulp/pulpcore"],
                "operations": ["clone", "push_branch", "create_pr_draft", "read_all"]
            }
        }
    }
}
```

### 6.3 Dashboard scope builder

The dashboard needs a UI component for building scopes. This is a form with:

1. **Service selector:** checkboxes for GitHub, GitLab.
2. **Repository list:** text inputs for repo patterns (with autocomplete from
   the credential's accessible repos, fetched via Gate test endpoint).
3. **Operation picker:** grouped checkboxes (Read / Write / Dangerous) with
   convenience presets (Read-Only, Contributor, Maintainer).
4. **Scope preview:** live JSON preview of the scope object.

This is a frontend-only change to the dashboard HTML/JS. The API does not need
modification.

### 6.4 Audit log enhancement

The existing proxy log infrastructure (`/api/v1/sessions/{id}/proxy-log`)
already captures all Gate decisions. SCM operations will appear with service
and operation fields populated:

```json
{
    "timestamp": "2026-03-25T10:30:00Z",
    "method": "POST",
    "url": "https://api.github.com/repos/pulp/pulpcore/pulls",
    "service": "github",
    "operation": "create_pr_draft",
    "decision": "allow",
    "status_code": 200,
    "session_id": "abc-123"
}
```

---

## 7. Security Model Summary

### Threat: Skiff extracts real credentials

**Mitigation:** Real credentials never enter the Skiff container. They exist
only in Gate's memory (loaded from encrypted env vars at startup). The Skiff
container receives dummy tokens that are random UUIDs with an `alcove-session-`
prefix.

### Threat: Skiff bypasses Gate to reach APIs directly

**Mitigation (Kubernetes):** NetworkPolicy restricts Skiff pod egress to only
the Gate sidecar (same pod, localhost) and allowed package registries.
`api.github.com` and `gitlab.com` API endpoints are not in the egress allowlist.

**Mitigation (podman):** `HTTP_PROXY`/`HTTPS_PROXY` is set to Gate, and
iptables rules block direct egress from the Skiff container to external hosts.
Git transport to `github.com:443` is allowed through CONNECT tunnels (Gate
validates the service is in scope), but API calls must go through the
`/github/` or `/gitlab/` proxy endpoints.

### Threat: Skiff sends unauthorized operations through Gate

**Mitigation:** Gate classifies every API request by HTTP method and URL path,
mapping it to an operation name. The operation is checked against the scope's
`operations` list. Requests for operations not in scope receive a 403.

### Threat: Scope escalation (e.g., `create_pr_draft` used to create a
non-draft PR)

> **Deferred to Phase 4.** Request body inspection for draft PR enforcement is
> not yet implemented. The `create_pr_draft` operation is recognized by the
> scope checker but Gate does not currently inspect or rewrite the request body
> to force `draft: true`.

**Planned mitigation:** For `create_pr_draft`, Gate would inspect the request
body to verify the `draft: true` field (GitHub) or `WIP:` title prefix
(GitLab). If the body does not indicate a draft, Gate would rewrite it to
force draft mode, or reject it.

### Threat: Dummy token leaked to external party

**Impact:** None. The dummy token is a random UUID prefixed with
`alcove-session-`. It is not accepted by any real API. It only has meaning
within the Gate sidecar of a single, ephemeral Skiff pod.

### Threat: Real credential exposed via Gate error messages

**Mitigation:** Gate never includes real credentials in error responses, log
messages, or any output visible to Skiff. Error messages reference the service
name and operation, never the credential value.

---

## 8. Implementation Plan

Ordered list of tasks with file paths. Each task is independently testable.

### Phase 1: Core SCM Proxy (MVP) -- IMPLEMENTED

**Task 1.1: Add SCM proxy endpoints to Gate**
- File: `internal/gate/proxy.go`
- Add `handleSCMProxy()` method
- Add `/github/` and `/gitlab/` route registrations in `Handler()`
- Add `GitLabHost` to `Config`
- Block CONNECT tunnels to `api.github.com` (but allow `github.com` for git)
- Tests: `internal/gate/proxy_test.go`

**Task 1.2: Enhance scope checker for SCM operations**
- File: `internal/gate/scope.go`
- Already largely implemented; add missing operations: `approve_pr`,
  `delete_branch`, `write_releases`, GitLab `notes` (comments), `approve`
- Add `create_pr` vs `create_pr_draft` distinction (body inspection)
- Tests: `internal/gate/scope_test.go`

**Task 1.3: Add SCM credential acquisition to CredentialStore**
- File: `internal/bridge/credentials.go`
- Add `AcquireSCMToken()` method
- Support `pat` and `project_token` auth types
- Tests: `internal/bridge/credentials_test.go`

**Task 1.4: Wire SCM credentials in Dispatcher**
- File: `internal/bridge/dispatcher.go`
- Resolve SCM credentials from CredentialStore
- Generate dummy tokens
- Set Skiff env vars (`GITHUB_TOKEN`, `GITHUB_API_URL`, etc.)
- Set Gate env vars (`GATE_CREDENTIALS` with real tokens)
- Tests: integration test with mock runtime

**Task 1.5: Gate env var loading for SCM config**
- File: `cmd/gate/main.go`
- Load `GATE_GITLAB_HOST` env var
- Tests: unit test for `loadConfig()`

### Phase 2: Git Transport -- IMPLEMENTED

**Task 2.1: Git credential helper script**
- File: `build/alcove-credential-helper`
- Shell script implementing git credential helper protocol
- Calls Gate's `/git-credential` endpoint

**Task 2.2: Install credential helper in Skiff image**
- File: `build/Containerfile.skiff-base`
- Copy credential helper script
- Configure `git config --system credential.helper alcove`

**Task 2.3: Configure git environment in skiff-init**
- File: `cmd/skiff-init/main.go`
- Set `GATE_CREDENTIAL_URL` env var for the credential helper
- Ensure `GIT_TERMINAL_PROMPT=0` (already done)

### Phase 3: CLI Tools -- IMPLEMENTED

**Task 3.1: Install gh and glab in Skiff image**
- File: `build/Containerfile.skiff-base`
- Add `gh` CLI (from GitHub RPM repo)
- Add `glab` CLI (from binary release)

**Task 3.2: Validate gh/glab work through Gate proxy**
- Manual testing: submit a task with GitHub scope, verify `gh pr create`
  works through Gate
- Verify `glab mr create` works through Gate

### Phase 4: Draft PR Enforcement -- DEFERRED

**Task 4.1: Request body inspection for PR creation**
- File: `internal/gate/proxy.go`
- In `handleSCMProxy`, inspect body for `create_pr_draft` operation
- Force `draft: true` (GitHub) or `WIP:` prefix (GitLab)
- Tests: `internal/gate/proxy_test.go`

### Phase 5: Dashboard -- DEFERRED

**Task 5.1: Scope builder UI component**
- File: `web/` (dashboard static files)
- Add scope builder form with service/repo/operation selection
- Add convenience presets (Read-Only, Contributor, Maintainer)

**Task 5.2: Credential management for SCM**
- File: `web/` (dashboard static files)
- Add GitHub/GitLab credential creation form
- Show credential type (LLM vs SCM) in credentials list

### Phase 6: Alias Expansion -- DEFERRED

**Task 6.1: Expand operation aliases in Bridge**
- File: `internal/bridge/dispatcher.go` or new `internal/scope_expand.go`
- Before passing scope to Gate, expand `read_all`, `contribute`, `maintain`
  into their constituent operations
- Tests: unit tests for alias expansion

---

## 9. Open Questions

1. **GitHub App vs PAT:** Should we prioritize GitHub App support (installation
   tokens with fine-grained permissions) or PATs (simpler, user-scoped)? PATs
   are simpler for Phase 1, but Apps provide better security isolation per-task.

2. **Push enforcement granularity:** Can we distinguish `push_branch` from
   `push_main` at the Gate level? With PATs, no -- the credential has the same
   permissions for all branches. With GitHub Apps, we could request different
   permission sets. With GitLab project tokens, branch protection rules on the
   server enforce this regardless.

3. **Self-hosted GitLab:** Should `GATE_GITLAB_HOST` support multiple GitLab
   instances, or is one sufficient? Multiple instances would require the scope
   to specify which instance each project belongs to.

4. **Rate limiting:** Should Gate enforce per-task rate limits on SCM API calls
   to prevent abuse? This is separate from GitHub/GitLab's own rate limits.

5. **Webhook verification:** If a task creates a PR, should Gate intercept the
   PR creation response and record the PR URL as an artifact? This would enable
   automatic artifact tracking without relying on Claude Code to report it.
