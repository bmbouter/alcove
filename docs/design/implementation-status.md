# Implementation Status

Last updated: 2026-03-30

## Project Overview

Alcove is an OpenShift/Kubernetes-native platform for running sandboxed AI coding
agents (Claude Code) in ephemeral containers. See [architecture.md](architecture.md)
for full design and [architecture-decisions.md](architecture-decisions.md) for
all resolved decisions.

## Component Names

| Component | Name | Purpose |
|-----------|------|---------|
| Controller | **Bridge** | REST API, dashboard, task dispatch, scheduler, admin settings, profile builder |
| Worker | **Skiff** | Ephemeral Claude Code execution |
| Auth Proxy | **Gate** | Sidecar proxy: token swap, LLM API proxy, SCM proxy (`/github/`, `/gitlab/`), scope enforcement, MCP tool configs |
| Message Bus | **Hail** | NATS-based status, transcript streaming, cancellation |
| Session Store | **Ledger** | PostgreSQL session records, transcripts, proxy logs |

## What's Built (Phase 1 — functional)

### Go Codebase (~10,000 lines Go, ~16,300 total with SQL/JS/HTML/CSS, compiles clean, tests pass)

```
alcove/
├── cmd/
│   ├── bridge/main.go          ✅ Controller: REST API, auth, task dispatch, migration, scheduling, admin settings
│   ├── gate/main.go            ✅ HTTP proxy with scope enforcement, LLM proxying, SCM proxying (/github/, /gitlab/), token refresh, MCP tool configs
│   ├── skiff-init/main.go      ✅ PID 1 init: reads task from env, runs claude, streams output, publishes transcript events to NATS
│   ├── hashpw/main.go          ✅ Utility: password hashing tool
│   └── alcove/main.go          ✅ CLI: 8 subcommands (run, list, logs, status, cancel, login, config, version)
├── internal/
│   ├── types.go                ✅ Shared types (Task, Session, Scope, TranscriptEvent, etc.)
│   ├── runtime/
│   │   ├── runtime.go          ✅ Runtime interface (RunTask, CancelTask, EnsureService, etc.)
│   │   ├── podman.go           ✅ PodmanRuntime implementation (podman CLI wrapper)
│   │   ├── podman_test.go      ✅ 14 tests (TestHelperProcess pattern)
│   │   ├── kubernetes.go       ✅ KubernetesRuntime implementation (client-go, Jobs with native sidecars, NetworkPolicy)
│   │   └── kubernetes_test.go  ✅ Kubernetes runtime tests
│   ├── bridge/
│   │   ├── api.go              ✅ REST handlers (tasks, sessions, schedules, credentials, providers, profiles, tools, settings, health, transcript SSE streaming, proxy-log ingestion)
│   │   ├── dispatcher.go       ✅ Task dispatch: creates session, resolves security profiles, publishes to NATS, starts Skiff+Gate with LLM/SCM credentials and tool configs
│   │   ├── config.go           ✅ Config loading from env vars, auth backend selection, debug mode
│   │   ├── runtime.go          ✅ Runtime factory (podman/kubernetes selection)
│   │   ├── credentials.go      ✅ Credential CRUD, AES-256-GCM encryption, OAuth2 token acquisition, token refresh, AcquireSCMToken, AcquireSystemToken, owner scoping, system credentials
│   │   ├── credentials_test.go ✅ Credential store tests
│   │   ├── scheduler.go        ✅ Cron scheduler: parsing, next-run computation, schedule CRUD, background tick loop, per-schedule debug flag
│   │   ├── profiles.go         ✅ Security profiles: multi-rule per-repo operation scoping, builtin profile seeding, profile CRUD with owner scoping
│   │   ├── tools.go            ✅ MCP tool registry: tool CRUD, MCP command/args, tool configs passed to Gate/Skiff
│   │   ├── settings.go         ✅ Admin settings: system LLM configuration, skill repos (system + per-user), env+DB+config resolution
│   │   ├── llm.go              ✅ BridgeLLM: system LLM client for AI-powered features (Anthropic + Vertex AI), used by profile builder
│   │   ├── migrate.go          ✅ Embedded SQL migration runner with advisory locking
│   │   └── migrations/
│   │       ├── 001_initial_schema.sql  ✅ Sessions, provider_credentials, auth_users, auth_sessions, schedules tables
│   │       ├── 002_session_token_and_indexes.sql  ✅ Session token column, performance indexes
│   │       ├── 003_user_admin_flag.sql  ✅ Admin role flag on auth_users
│   │       ├── 004_resource_owners.sql  ✅ Owner columns on credentials and schedules
│   │       ├── 005_schedule_debug.sql  ✅ Debug flag on schedules
│   │       ├── 006_mcp_tools.sql       ✅ MCP tool registry table
│   │       ├── 007_security_profiles.sql  ✅ Security profiles table
│   │       ├── 008_credential_api_host.sql  ✅ Custom API host for credentials (GitLab private servers)
│   │       └── 009_system_settings.sql  ✅ System settings key-value store
│   ├── gate/
│   │   ├── proxy.go            ✅ HTTP proxy, CONNECT tunneling, LLM API injection (api_key + bearer), audit logging, 401 token refresh
│   │   ├── proxy_test.go       ✅ Proxy tests
│   │   └── scope.go            ✅ Scope enforcement, GitHub/GitLab URL parsing, git credential helper
│   ├── hail/
│   │   └── client.go           ✅ NATS client wrapper (connect, subscribe, publish status/transcript, cancel)
│   ├── ledger/
│   │   └── client.go           ✅ HTTP client for session CRUD + transcript streaming
│   └── auth/
│       ├── auth.go             ✅ Authenticator + UserManager interfaces, Argon2id passwords, LoginHandler, AuthMiddleware, admin role checks, SSE query-param token fallback
│       ├── memory.go           ✅ MemoryStore: in-memory auth backend with rate limiting
│       ├── postgres.go         ✅ PgStore: PostgreSQL-backed auth with user CRUD, admin flag, session persistence, expired session cleanup, password change
│       └── users_api.go        ✅ User management HTTP handlers (list, create, delete, change password, set admin role)
├── web/
│   ├── index.html              ✅ Dashboard SPA shell with all page views, setup checklist
│   ├── css/style.css           ✅ Dark theme dashboard styles
│   └── js/app.js               ✅ Full SPA: login, session list with pagination, new task form with profile selection, live transcript viewer (SSE with catch-up + live), proxy log viewer, providers page, security profiles page, MCP tools page, schedules with NLP cron input, admin settings, user management, guided setup checklist, contextual warnings
├── build/
│   ├── Containerfile.bridge    ✅ Multi-stage (golang:1.25 → ubi9/ubi)
│   ├── Containerfile.gate      ✅ Multi-stage (golang:1.25 → ubi9-minimal)
│   ├── alcove-credential-helper ✅ Git credential helper binary (used by Skiff for HTTPS git auth via Gate)
│   └── Containerfile.skiff-base ✅ Multi-stage (golang:1.25 → ubi9/ubi + nodejs + claude-code + gh + glab + credential helper)
├── docs/
│   ├── getting-started.md      ✅ 5-minute quick start guide
│   └── design/
│       ├── implementation-status.md    ✅ This file
│       ├── architecture.md             ✅ Full component design
│       ├── architecture-decisions.md   ✅ 18 resolved decisions
│       ├── problem-statement.md        ✅ Why ephemeral agents
│       ├── credential-management.md    ✅ Credential storage and token flow design
│       ├── auth-backends.md            ✅ Dual auth backend design
│       ├── mcp-tool-gateway.md         ✅ MCP tool gateway design
│       └── security-profiles-and-system-llm.md  ✅ Security profiles and system LLM design
├── Makefile                    ✅ build, build-images, up, down, logs, dev-up, dev-infra, dev-down, dev-logs, dev-reset, test, lint
├── LICENSE                     ✅ Apache-2.0
├── go.mod                      ✅ github.com/bmbouter/alcove (Go 1.25)
└── go.sum                      ✅ Dependencies resolved
```

### Container Images Built

- `localhost/alcove-bridge:dev` ✅ (Bridge controller + dashboard)
- `localhost/alcove-gate:dev` ✅
- `localhost/alcove-skiff-base:dev` ✅ (includes Claude Code CLI via npm)

### Infrastructure Tested

- `make dev-up` ✅ — starts Bridge + NATS + PostgreSQL on podman networks `alcove-internal` + `alcove-external`
- `make up` ✅ — builds all images and starts the full environment
- Bridge starts, connects to NATS + PostgreSQL ✅
- Database migrations run automatically on startup ✅
- Auth works (login, token, rate limiting) with both memory and postgres backends ✅
- `POST /api/v1/tasks` creates session in DB, publishes to NATS, starts Gate + Skiff containers ✅
- Skiff containers boot, read task from env vars, attempt to run Claude Code ✅
- LLM credential flow works end-to-end: Bridge acquires tokens, Gate injects headers ✅
- Containers exit and are cleaned up by `--rm` (or kept with `ALCOVE_DEBUG=true`) ✅
- Dashboard accessible at http://localhost:8080 ✅
- `make test-network` ✅ — verifies dual-network isolation (Skiff cannot reach internet, Gate can)

### What's Working End-to-End

1. **LLM Credential Flow** — Full credential management with encrypted storage in
   PostgreSQL (AES-256-GCM). Supports Anthropic API keys, Vertex AI service accounts,
   and ADC. Bridge pre-fetches OAuth2 tokens at dispatch time. Gate receives only
   short-lived tokens and can refresh via Bridge's internal token-refresh endpoint.
   Dispatcher injects `GATE_LLM_TOKEN`, `GATE_LLM_PROVIDER`, `GATE_LLM_TOKEN_TYPE`
   into Gate env and `ANTHROPIC_BASE_URL` into Skiff env pointing at the Gate sidecar.

2. **Dashboard Frontend** — Full SPA in `web/` with login form, session list with
   status filters, search, and pagination, new task form with provider and profile
   selection and debug toggle, session detail view with live transcript streaming
   (SSE with catch-up + live) and proxy log tabs, providers page, security profiles
   page, MCP tools page, schedules page with NLP-style cron input, admin settings
   page, user management page, guided setup checklist with contextual warnings.
   Dark theme.

3. **Ledger Ingestion API** — Bridge serves POST endpoints for transcript, status,
   and proxy-log ingestion:
   - `POST /api/v1/sessions/{id}/transcript` — append transcript events (atomic JSONB append)
   - `POST /api/v1/sessions/{id}/status` — update session outcome, exit code, artifacts
   - `POST /api/v1/sessions/{id}/proxy-log` — append proxy log entries
   - `GET /api/v1/sessions/{id}/proxy-log` — retrieve proxy log

4. **Cron Scheduler** — Full cron scheduler with 5-field expression parsing (wildcards,
   ranges, steps, lists), schedule CRUD API, background tick loop (60s interval),
   automatic next-run computation, per-schedule debug flag, owner scoping. Dashboard
   includes NLP-style cron expression input (e.g., "every weekday at 9am",
   "twice daily", "hourly on weekdays").

5. **Credential Management API** — CRUD for provider credentials, encrypted storage,
   token acquisition (API key pass-through or OAuth2 exchange), env-based credential
   migration on first start, owner scoping (user credentials vs system credentials),
   system credentials (`_system` owner) for Bridge-level LLM features, custom
   `api_host` field for GitLab private server support.

6. **Dual Auth Backends** — `AUTH_BACKEND=memory` (default, in-memory) or
   `AUTH_BACKEND=postgres` (persistent users/sessions in PostgreSQL). Both support
   Argon2id passwords and rate limiting. Postgres backend adds user CRUD API,
   admin role management, self-service password change, and session persistence
   with hourly cleanup.

7. **Database Migrations** — Custom migration runner using embedded SQL files with
   PostgreSQL advisory locking to prevent concurrent startup races. Schema versioning
   via `schema_migrations` table.

8. **Debug Mode** — `ALCOVE_DEBUG=true` or `--debug` flag on task submission keeps
   Skiff containers after exit for log inspection.

9. **Gate Vertex API Translation** — Gate translates Anthropic API requests to
   Vertex AI format when the provider is `google-vertex`: URL rewriting to Vertex
   AI endpoints, body transformation (removes `model` and `context_management`,
   adds `anthropic_version`), model name conversion, and header stripping.

10. **SCM Authorization** — Gate provides `/github/` and `/gitlab/` reverse-proxy
    endpoints that forward requests to the respective SCM APIs after operation-level
    scope checking. Bridge resolves SCM credentials via `AcquireSCMToken` at dispatch
    time and injects dummy tokens into the Skiff container. Gate swaps the dummy
    tokens for real PATs at proxy time. The Skiff base image includes the `gh` and
    `glab` CLIs, and a git credential helper (`build/alcove-credential-helper`) that
    acquires HTTPS git credentials from Gate. SCM-related environment variables
    (`GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN`, `GITHUB_API_URL`, `GITLAB_API_URL`,
    `GH_HOST`, `GLAB_HOST`, `GATE_CREDENTIAL_URL`, `GIT_SSH_COMMAND`, etc.) are
    injected by Bridge when the task scope includes a `github` or `gitlab` service.

11. **Real-Time Transcript Streaming** — Skiff publishes transcript events to NATS
    via `hail.PublishTranscript()`. Bridge subscribes to the NATS subject for the
    session and streams events to the dashboard via SSE. The SSE endpoint implements
    catch-up + live: on connect, it sends all persisted transcript events from the
    database, then subscribes to NATS for live events. Status updates are also
    streamed so the client detects session completion. Auth supports query-param
    token fallback for `EventSource` (which cannot set HTTP headers).

12. **Security Profiles** — Named, reusable bundles of tool + repo + operation
    permissions. Supports multi-rule per-repo operation scoping (each rule specifies
    repos and operations independently). Profiles have owner scoping and builtin
    profiles are seeded on startup. Dispatcher resolves security profiles into scope
    and tool configs at dispatch time. Profile CRUD API with owner isolation.

13. **AI-Powered Profile Builder** — `POST /api/v1/profiles/build` accepts a natural
    language description and uses the system LLM (BridgeLLM) to generate a JSON
    security profile. BridgeLLM supports both Anthropic and Vertex AI providers
    and acquires credentials from the credential store (including system credentials).

14. **MCP Tool Gateway** — Tool registry for MCP tools with CRUD API. Tools define
    MCP command, args, display name, and optional API host. Tool configs from
    security profiles are resolved at dispatch time and passed to Gate/Skiff
    containers. Database schema in migration 006.

15. **Admin Settings** — System LLM configuration stored in the database
    (`system_settings` table). Settings resolve with a layered precedence:
    database > environment variables. Admin settings API for get/set with
    source tracking. Used by BridgeLLM for AI-powered features.

16. **Dashboard Guided Configuration** — Setup checklist on the dashboard that
    tracks configuration completeness (credentials, profiles, etc.) with
    contextual warnings. Dismissable by the user.

17. **User Management with Admin Roles** — Admin flag on users (migration 003).
    Admin-only API endpoints for user CRUD and role management. Non-admin users
    can still change their own password (self-service password change).

18. **Session Pagination** — Session list API supports `per_page` and page-based
    pagination for large session histories.

19. **Skill/Agent Repos** — Git repositories containing Claude Code plugins
    (skills and agents) that are loaded into Skiff containers. System-wide
    (admin) and per-user configuration via `GET/PUT /api/v1/admin/settings/skill-repos`
    and `GET/PUT /api/v1/user/settings/skill-repos`. At dispatch time, Bridge
    merges both lists and passes them to Skiff as `ALCOVE_SKILL_REPOS`. Skiff
    clones each repo and passes them to Claude Code via `--plugin-dir` flags.
    Plugin structure: `.claude-plugin/plugin.json` with `skills/` and `agents/`
    directories.

20. **YAML Task Definitions** — Tasks defined in `.alcove/tasks/*.yml` in git
    repos. Task repo registration (system + per-user) via settings API.
    YAML schema supports name, prompt, repo, provider, model, timeout, budget,
    profiles, tools, and schedule fields. Auto-sync every 5 minutes. Dashboard
    supports Run Now and View YAML actions. Starter templates available via
    `GET /api/v1/task-templates`.

21. **Kubernetes Runtime** — `KubernetesRuntime` in `internal/runtime/kubernetes.go`
    implements the `Runtime` interface using direct client-go API calls (no
    operator needed). Each task runs as a k8s Job with Gate as a native sidecar
    (init container with `restartPolicy: Always`) and Skiff as the main
    container. Creates a per-task NetworkPolicy restricting egress. Compatible
    with OpenShift restricted-v2 SCC (runs as non-root, drops all capabilities,
    sets `seccompProfile: RuntimeDefault`). Minimal RBAC: Bridge needs create/delete
    permissions for Jobs and NetworkPolicies.

22. **CI/CD** — GitHub Actions workflows for testing (`ci.yml`) and releasing
    (`release.yml`). Container images published to `ghcr.io/bmbouter`.
    v0.1.0 released.

## What's NOT Working Yet

### 1. NATS Dead Code

The dispatcher still publishes to `tasks.dispatch` on NATS (line 125 of
dispatcher.go) but nothing subscribes — Skiff reads tasks from environment
variables, not NATS. This publish call is the remaining dead code.

### 2. Gate Log Flushing to Ledger

Gate has `StartLogFlusher()` and `sendLogsToLedger()` but the actual HTTP POST
to Bridge's proxy-log ingestion endpoint is not yet implemented (marked with
a TODO in `proxy.go`). Proxy logs are currently buffered in Gate's memory but
not sent to Ledger automatically.

### 3. CLI End-to-End Verification

The CLI (`cmd/alcove/main.go`) is fully implemented with 8 subcommands but has
not been systematically tested against a running Bridge instance. Individual
commands (`run`, `list`, `logs`, `status`, `cancel`, `login`) need verification.

## How to Run (Developer Workflow)

See [getting-started.md](../getting-started.md) for the full quick-start guide.

### Fastest Path

```bash
cd ~/devel/alcove

# Build images and start everything
make up

# Dashboard is at http://localhost:8080
# Log in with admin/admin, then change the password
```

### Development Mode (Bridge runs locally)

```bash
cd ~/devel/alcove

# 1. Start infrastructure only
make dev-infra

# 2. Build Go binaries
make build

# 3. Run Bridge locally
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
BRIDGE_PORT=8080 \
SKIFF_IMAGE="localhost/alcove-skiff-base:dev" \
GATE_IMAGE="localhost/alcove-gate:dev" \
./bin/bridge

# 4. In another terminal, use the CLI or curl
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

curl -s -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Say hello","provider":"anthropic","timeout":300,"scope":{"services":{}}}'

# 5. Rebuild images after code changes
make build-images
```

## Next Steps (Priority Order)

### Short-term (Phase 1 completion)

1. **Wire Gate log flushing** — implement the HTTP POST in `sendLogsToLedger()`
   to send proxy log entries to Bridge's `/api/v1/sessions/{id}/proxy-log` endpoint.

2. **CLI testing** — verify `alcove run`, `alcove list`, `alcove logs`, `alcove status`,
   `alcove cancel` work against a running Bridge.

3. **Clean up dead code** — remove `SubscribeTaskDispatch()` from hail client.

### Medium-term (Phase 2)

4. **Session artifacts** — structured output (PRs, patches, commits) with
   links back to source services.

5. **Credential rotation and expiry** — notifications for expiring credentials,
   automatic rotation support.

See the full roadmap in [architecture-decisions.md](architecture-decisions.md#roadmap-revised).

## Key Design Documents

- [architecture.md](architecture.md) — full component design, deployment diagrams, network isolation
- [architecture-decisions.md](architecture-decisions.md) — 18 resolved decisions, CLI design, config format, repo layout, revised roadmap
- [problem-statement.md](problem-statement.md) — why ephemeral agents (context contamination, credential drift, filesystem poisoning, credential exposure)
- [credential-management.md](credential-management.md) — credential storage, encryption, OAuth2 token flow, token refresh design
- [auth-backends.md](auth-backends.md) — dual auth backend design (memory vs postgres)
- [podman-network-isolation.md](podman-network-isolation.md) — dual-network isolation design with `--internal` flag on podman
- [gate-scm-authorization.md](gate-scm-authorization.md) — SCM authorization design: Gate proxy endpoints, scope checking, credential resolution, git credential helper
- [mcp-tool-gateway.md](mcp-tool-gateway.md) — MCP tool gateway design
- [security-profiles-and-system-llm.md](security-profiles-and-system-llm.md) — Security profiles and system LLM design

## Dependencies

| Dependency | Version | Purpose |
|-----------|---------|---------|
| `github.com/nats-io/nats.go` | v1.50.0 | NATS client for Hail |
| `github.com/jackc/pgx/v5` | v5.9.1 | PostgreSQL client for Ledger |
| `github.com/spf13/cobra` | v1.10.2 | CLI framework |
| `github.com/google/uuid` | v1.6.0 | Session/task ID generation |
| `golang.org/x/crypto` | v0.49.0 | Argon2id password hashing |
| `golang.org/x/oauth2` | — | Google OAuth2 token acquisition (Vertex AI) |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML parsing |

## Environment Variables

### Bridge

| Variable | Default | Description |
|----------|---------|-------------|
| `LEDGER_DATABASE_URL` | (required) | PostgreSQL connection string |
| `HAIL_URL` | (required) | NATS server URL |
| `RUNTIME` | `podman` | Container runtime (`podman` or `kubernetes`) |
| `BRIDGE_PORT` | `8080` | HTTP server port |
| `SKIFF_IMAGE` | `localhost/alcove-skiff-base:dev` | Skiff container image |
| `GATE_IMAGE` | `localhost/alcove-gate:dev` | Gate container image |
| `ALCOVE_NETWORK` | `alcove-internal` | Podman internal network name |
| `ALCOVE_EXTERNAL_NETWORK` | `alcove-external` | Podman external network name (Gate egress) |
| `AUTH_BACKEND` | `memory` | Auth backend: `memory` or `postgres` |
| `ALCOVE_DATABASE_ENCRYPTION_KEY` | (insecure default) | Master key for credential encryption (AES-256) |
| `ALCOVE_DEBUG` | (unset) | Set to any value to enable debug mode (keep containers after exit) |
| `ALCOVE_WEB_DIR` | `web` | Path to dashboard static files |
| `ANTHROPIC_API_KEY` | (optional) | Anthropic API key (auto-migrated to credential store) |
| `VERTEX_API_KEY` | (optional) | Vertex AI API key (auto-migrated to credential store) |
| `VERTEX_PROJECT` | (optional) | GCP project ID for Vertex AI provider |
| `ANTHROPIC_MODEL` | `claude-sonnet-4-20250514` | Default model for Anthropic provider |
| `VERTEX_MODEL` | `claude-sonnet-4-20250514` | Default model for Vertex AI provider |
| `BRIDGE_URL` | `http://alcove-bridge:<port>` | Bridge URL used for Gate token refresh callbacks |

### Skiff (injected by Bridge)

| Variable | Description |
|----------|-------------|
| `TASK_ID` | Unique task identifier |
| `SESSION_ID` | Session identifier for Ledger |
| `PROMPT` | The Claude Code prompt to execute |
| `REPO` | Git repository URL to clone (optional) |
| `CLAUDE_MODEL` | LLM model name |
| `TASK_BUDGET` | Max spend in USD |
| `TASK_TIMEOUT` | Timeout in seconds |
| `HAIL_URL` | NATS server for status updates |
| `LEDGER_URL` | Bridge HTTP URL for transcript and status writes |
| `SESSION_TOKEN` | Auth token for Ledger writes |
| `HTTP_PROXY` | Points to Gate container |
| `HTTPS_PROXY` | Points to Gate container |
| `NO_PROXY` | Addresses excluded from proxy |
| `ANTHROPIC_BASE_URL` | Points to Gate container (`http://gate-<taskID>:8443`) for LLM API proxying |
| `ANTHROPIC_API_KEY` | Placeholder API key (real key injected by Gate) |
| `GITHUB_TOKEN` | Dummy GitHub token (Gate swaps for real PAT) |
| `GH_TOKEN` | Alias for `GITHUB_TOKEN` used by `gh` CLI |
| `GITHUB_PERSONAL_ACCESS_TOKEN` | Alias for GitHub tooling |
| `GITHUB_API_URL` | Points to Gate's `/github/` proxy endpoint |
| `GH_HOST` | GitHub host for `gh` CLI |
| `GH_PROMPT_DISABLED` | Disables interactive prompts in `gh` CLI |
| `GH_NO_UPDATE_NOTIFIER` | Disables `gh` update notifications |
| `GITLAB_TOKEN` | Dummy GitLab token (Gate swaps for real PAT) |
| `GITLAB_PERSONAL_ACCESS_TOKEN` | Alias for GitLab tooling |
| `GITLAB_API_URL` | Points to Gate's `/gitlab/` proxy endpoint |
| `GLAB_HOST` | GitLab host for `glab` CLI |
| `GATE_CREDENTIAL_URL` | Gate endpoint for git credential helper |
| `GIT_SSH_COMMAND` | Disables SSH git operations (forces HTTPS via Gate) |

### Gate (injected by Bridge)

| Variable | Description |
|----------|-------------|
| `GATE_SESSION_ID` | Session identifier |
| `GATE_SCOPE` | JSON-encoded authorization scope |
| `GATE_SESSION_TOKEN` | Opaque token presented by Skiff |
| `GATE_CREDENTIALS` | JSON map of service → real credential |
| `GATE_LLM_TOKEN` | LLM bearer token or API key (pre-fetched by Bridge) |
| `GATE_LLM_PROVIDER` | `anthropic` or `google-vertex` |
| `GATE_LLM_TOKEN_TYPE` | `api_key` or `bearer` |
| `GATE_TOKEN_REFRESH_URL` | Bridge endpoint for OAuth2 token refresh |
| `GATE_TOKEN_REFRESH_SECRET` | Session-scoped secret for refresh authentication |
| `GATE_LEDGER_URL` | Bridge URL for proxy log writes |
| `GATE_VERTEX_REGION` | Vertex AI region (default `us-east5`) |
| `GATE_VERTEX_PROJECT` | GCP project ID for Vertex AI |
| `GATE_GITLAB_HOST` | GitLab hostname for self-hosted instances (default `gitlab.com`) |
