# Configuration Reference

This document covers every configuration option for Alcove's three components
(Bridge, Gate, Skiff) and the CLI client.

## Config Hierarchy

Bridge configuration comes from three sources (highest to lowest priority):

1. **Environment variables** -- always take precedence over config file values
2. **Config file (`alcove.yaml`)** -- infrastructure settings and system LLM configuration
3. **Dashboard / API** -- credentials, providers, users, security profiles

The default admin account is `admin` / `admin`. Change the password in the
dashboard after first login.

### alcove.yaml

The `alcove.yaml` file provides a persistent location for infrastructure-level
Bridge settings. It uses YAML syntax:

```yaml
database_encryption_key: your-aes-256-key-here
database_url: postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable
nats_url: nats://localhost:4222
auth_backend: memory
port: 8080
runtime: podman

# System LLM configuration (choose one provider)
# Option A: Anthropic API
llm_provider: anthropic
llm_api_key: sk-ant-...
llm_model: claude-sonnet-4-20250514

# Option B: Google Vertex AI
# llm_provider: google-vertex
# llm_service_account_json: '{"type":"service_account","project_id":"...","private_key":"...",...}'
# llm_project: your-gcp-project-id
# llm_region: us-east5
# llm_model: claude-sonnet-4-20250514
```

**Search order:** Bridge looks for the config file in this order:

1. Path specified by `ALCOVE_CONFIG_FILE` environment variable
2. `./alcove.yaml` (current working directory)
3. `/etc/alcove/alcove.yaml`

Environment variables always override values from the config file. For example,
if `alcove.yaml` sets `port: 8080` but `BRIDGE_PORT=9090` is in the environment,
Bridge listens on port 9090.

**Required database encryption key:** Bridge requires `database_encryption_key` (or the
`ALCOVE_DATABASE_ENCRYPTION_KEY` environment variable) to be set. Bridge refuses to
start without it. For local development, `make up` auto-generates `alcove.yaml`
from `alcove.yaml.example` with a random key. For Kubernetes deployments,
provide `ALCOVE_DATABASE_ENCRYPTION_KEY` via a k8s Secret (see
[Kubernetes](#kubernetes) below).

The `alcove.yaml` file is gitignored. An `alcove.yaml.example` is committed to
the repository as a reference.

For the CLI client the resolution order is:

1. Config file (`~/.config/alcove/config.yaml`)
2. Environment variable (`ALCOVE_SERVER`)
3. CLI flag (`--server`)

---

## Bridge Environment Variables

These variables configure the Bridge controller (`cmd/bridge`). The first six
can also be set in `alcove.yaml` (see [alcove.yaml](#alcoveyaml) above).

| Variable | Type | Default | Description |
|---|---|---|---|
| `HAIL_URL` | string | `nats://localhost:4222` | NATS server URL for the Hail message bus. |
| `LEDGER_DATABASE_URL` | string | `postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable` | PostgreSQL connection string for the Ledger session store. |
| `BRIDGE_PORT` | string | `8080` | HTTP listen port for the Bridge API and dashboard. |
| `RUNTIME` | string | `podman` | Container runtime. Must be `podman` or `kubernetes`. |
| `AUTH_BACKEND` | string | `memory` | Authentication backend. Must be `memory`, `postgres`, or `rh-identity`. See [Auth Backend Selection](#auth-backend-selection). |
| `RH_IDENTITY_ADMINS` | string | _(unset)_ | Comma-separated list of usernames (emails) to bootstrap as admins when using `rh-identity` backend. |
| `ALCOVE_DATABASE_ENCRYPTION_KEY` | string | _(required)_ | Encryption key for the credential store. **Bridge refuses to start without this.** For local dev, `make up` generates it automatically. |
| `ALCOVE_DEBUG` | string | _(unset)_ | Any non-empty value enables debug mode (keeps worker containers after exit). |
| `ALCOVE_WEB_DIR` | string | `web` | Directory containing dashboard static files. |
| `ANTHROPIC_API_KEY` | string | _(unset)_ | Anthropic API key. Auto-migrated to credential store on startup. |
| `ANTHROPIC_MODEL` | string | `claude-sonnet-4-20250514` | Default model for the Anthropic provider. |
| `VERTEX_PROJECT` | string | _(unset)_ | Google Cloud project ID. Registers the `vertex` provider when set. |
| `VERTEX_API_KEY` | string | _(unset)_ | API key for Google Vertex AI. Auto-migrated to credential store on startup. |
| `VERTEX_MODEL` | string | `claude-sonnet-4-20250514` | Default model for the Vertex AI provider. |
| `SKIFF_IMAGE` | string | `localhost/alcove-skiff-base:dev` | Container image for Skiff workers. |
| `GATE_IMAGE` | string | `localhost/alcove-gate:dev` | Container image for Gate sidecars. |
| `ALCOVE_NETWORK` | string | `alcove-internal` | Podman network name for internal container networking (created with `--internal` flag, no external access). |
| `ALCOVE_EXTERNAL_NETWORK` | string | `alcove-external` | External podman network for Gate egress. Gate bridges both networks; Skiff is attached only to the internal network. |
| `BRIDGE_URL` | string | `http://alcove-bridge:<port>` | URL where Bridge can be reached by Skiff/Gate containers. |
| `SKIFF_HAIL_URL` | string | `nats://alcove-hail:4222` | NATS URL injected into Skiff containers (may differ from Bridge's own `HAIL_URL`). |
| `ALCOVE_SKILL_REPOS` | string (JSON) | _(unset)_ | JSON array of skill repo objects. Overrides database-configured skill repos. Each object has `url` (required), `ref` (optional, default `main`), and `name` (optional). |
| `TASK_REPO_SYNC_INTERVAL` | string (duration) | `5m` | How often Bridge syncs YAML agent definitions from registered agent repos. Accepts Go duration syntax. |
| `BRIDGE_LLM_PROVIDER` | string | _(unset)_ | System LLM provider: `anthropic` or `google-vertex`. Overrides `llm_provider` in alcove.yaml. |
| `BRIDGE_LLM_API_KEY` | string | _(unset)_ | Anthropic API key for the system LLM. Overrides `llm_api_key` in alcove.yaml. |
| `BRIDGE_LLM_MODEL` | string | _(unset)_ | Model name for the system LLM. Overrides `llm_model` in alcove.yaml. |
| `BRIDGE_LLM_SERVICE_ACCOUNT_JSON` | string | _(unset)_ | Google service account JSON for the system LLM (Vertex AI). Overrides `llm_service_account_json` in alcove.yaml. |
| `BRIDGE_LLM_PROJECT` | string | _(unset)_ | GCP project ID for the system LLM (Vertex AI). Overrides `llm_project` in alcove.yaml. |
| `BRIDGE_LLM_REGION` | string | _(unset)_ | GCP region for the system LLM (Vertex AI). Overrides `llm_region` in alcove.yaml. |

---

## Gate Environment Variables

Gate is the authorization proxy sidecar (`cmd/gate`). These variables are
**injected by Bridge** into each Skiff pod. Operators do not set them directly;
they are documented here for debugging and for custom deployment scenarios.

| Variable | Type | Default | Description |
|---|---|---|---|
| `GATE_SESSION_ID` | string | _(required)_ | Session ID for this task. |
| `GATE_SCOPE` | string (JSON) | _(required)_ | JSON-encoded scope defining allowed services, repositories, and operations. |
| `GATE_CREDENTIALS` | string (JSON) | `{}` | JSON map of service name to real credential. Gate swaps session tokens for these. |
| `GATE_SESSION_TOKEN` | string | _(unset)_ | Opaque token that the Skiff container presents to Gate for authentication. |
| `GATE_LLM_TOKEN` | string | _(unset)_ | Bearer token or API key for the LLM provider. Falls back to `GATE_LLM_API_KEY`. |
| `GATE_LLM_API_KEY` | string | _(unset)_ | Legacy fallback for `GATE_LLM_TOKEN`. |
| `GATE_LLM_PROVIDER` | string | `anthropic` | LLM provider type. Either `anthropic` or `google-vertex`. |
| `GATE_LLM_TOKEN_TYPE` | string | `api_key` | How the LLM token is sent. Either `api_key` or `bearer`. |
| `GATE_TOKEN_REFRESH_URL` | string | _(unset)_ | Bridge endpoint URL for token refresh requests. |
| `GATE_TOKEN_REFRESH_SECRET` | string | _(unset)_ | Session-scoped secret used to authenticate token refresh requests. |
| `GATE_LEDGER_URL` | string | _(unset)_ | URL where Gate sends proxy audit logs. |
| `GATE_VERTEX_REGION` | string | `us-east5` | Vertex AI region for API URL construction. |
| `GATE_VERTEX_PROJECT` | string | _(unset)_ | Vertex AI project ID for API URL construction. |
| `GATE_GITLAB_HOST` | string | `gitlab.com` | GitLab hostname for self-hosted GitLab instances. Used to route `/gitlab/` proxy requests to the correct host. |

Gate listens on port **8443** inside the pod.

---

## Skiff Environment Variables

Skiff is the ephemeral worker container (`cmd/skiff-init`). These variables are
**injected by Bridge** when the container is created.

| Variable | Type | Default | Description |
|---|---|---|---|
| `TASK_ID` | string | _(required)_ | Unique identifier for the task. |
| `SESSION_ID` | string | value of `TASK_ID` | Session identifier. Defaults to the task ID if not set separately. |
| `PROMPT` | string | _(required)_ | The natural-language prompt sent to Claude Code. |
| `REPO` | string | _(unset)_ | Git repository URL to clone into the workspace. |
| `BRANCH` | string | _(unset)_ | Branch to check out after cloning. |
| `PROVIDER` | string | `anthropic` | LLM provider name. |
| `CLAUDE_MODEL` | string | _(unset)_ | Model override passed to `claude --model`. |
| `TASK_BUDGET` | string (float) | _(unset)_ | Maximum spend in USD passed to `claude --max-budget-usd`. |
| `TASK_TIMEOUT` | string (int) | `3600` | Hard timeout in seconds. The process is killed after this duration. |
| `HEARTBEAT_TIMEOUT` | string (duration) | `10m` | Maximum time without stdout output before the process is terminated. Accepts Go duration syntax (e.g., `5m`, `15m`). |
| `HAIL_URL` | string | `nats://localhost:4222` | NATS server URL for status updates and cancellation. Bridge-injected default: `nats://alcove-hail:4222`. |
| `LEDGER_URL` | string | `http://localhost:8081` | Ledger API URL for transcript storage. Bridge-injected default: `http://alcove-bridge:8080`. |
| `SESSION_TOKEN` | string | _(unset)_ | Token used to authenticate with the Ledger API. |
| `HTTP_PROXY` | string | _(injected)_ | Points to Gate container (`http://gate-<taskID>:8443`). Routes all HTTP traffic through Gate. |
| `HTTPS_PROXY` | string | _(injected)_ | Points to Gate container. Routes all HTTPS traffic through Gate. |
| `NO_PROXY` | string | _(injected)_ | Internal services exempt from proxy (includes Gate container name). |
| `ANTHROPIC_BASE_URL` | string | _(injected)_ | Points to Gate for LLM API proxying (`http://gate-<taskID>:8443`). |
| `ANTHROPIC_API_KEY` | string | `sk-placeholder-routed-through-gate` | Placeholder key that satisfies Claude Code validation. Real key is held by Gate. |
| `ALCOVE_SKILL_REPOS` | string (JSON) | _(injected)_ | JSON array of skill repo objects. Skiff clones each repo and passes them to Claude Code via `--plugin-dir` flags. |
| `ALCOVE_PLUGINS` | string (JSON) | _(injected)_ | JSON array of plugin specs from the agent definition. Skiff installs each plugin at startup (marketplace, official, or git-sourced). |

The following SCM-related environment variables are injected by Bridge when the
task's scope includes a `github` or `gitlab` service. They configure the `gh`
and `glab` CLIs and the git credential helper inside the Skiff container.

| Variable | Type | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` | string | _(injected)_ | Dummy GitHub token. Routed through Gate which swaps it for the real PAT. |
| `GH_TOKEN` | string | _(injected)_ | Alias for `GITHUB_TOKEN` used by the `gh` CLI. |
| `GITHUB_PERSONAL_ACCESS_TOKEN` | string | _(injected)_ | Alias recognized by some GitHub tooling. |
| `GITHUB_API_URL` | string | _(injected)_ | Points to Gate's `/github/` proxy endpoint (e.g., `http://gate-<taskID>:8443/github`). |
| `GH_HOST` | string | _(injected)_ | GitHub host for `gh` CLI (e.g., `github.com`). |
| `GH_PROMPT_DISABLED` | string | `1` | Disables interactive prompts in `gh` CLI. |
| `GH_NO_UPDATE_NOTIFIER` | string | `1` | Disables `gh` CLI update notifications. |
| `GITLAB_TOKEN` | string | _(injected)_ | Dummy GitLab token. Routed through Gate which swaps it for the real PAT. |
| `GITLAB_PERSONAL_ACCESS_TOKEN` | string | _(injected)_ | Alias recognized by some GitLab tooling. |
| `GITLAB_API_URL` | string | _(injected)_ | Points to Gate's `/gitlab/` proxy endpoint (e.g., `http://gate-<taskID>:8443/gitlab`). |
| `GLAB_HOST` | string | _(injected)_ | GitLab host for `glab` CLI (e.g., `gitlab.com`). |
| `JIRA_TOKEN` | string | _(injected)_ | Dummy JIRA token. Routed through Gate which swaps it for real credentials (Basic auth). |
| `JIRA_API_URL` | string | _(injected)_ | Points to Gate's `/jira/` proxy endpoint (e.g., `http://gate-<taskID>:8443/jira`). |
| `GATE_CREDENTIAL_URL` | string | _(injected)_ | Gate endpoint URL used by the git credential helper to acquire tokens. |
| `GIT_SSH_COMMAND` | string | _(injected)_ | Set to disable SSH-based git operations (forces HTTPS through Gate). |

Skiff also sets these git environment variables automatically (unless already
present):

| Variable | Default |
|---|---|
| `GIT_TERMINAL_PROMPT` | `0` |
| `GIT_AUTHOR_NAME` | `Alcove` |
| `GIT_AUTHOR_EMAIL` | `alcove@localhost` |
| `GIT_COMMITTER_NAME` | `Alcove` |
| `GIT_COMMITTER_EMAIL` | `alcove@localhost` |

---

## CLI Configuration

The `alcove` CLI (`cmd/alcove`) stores configuration in
`$XDG_CONFIG_HOME/alcove/` (defaults to `~/.config/alcove/`).

### Files

| File | Purpose |
|---|---|
| `config.yaml` | Stores the Bridge server URL and active team. Created by `alcove login`. |
| `credentials` | Stores the JWT authentication token. Created by `alcove login`. |

The `config.yaml` file supports an `active_team` field that sets the default
team context for all CLI commands. Set it with `alcove teams use`:

```yaml
server: http://localhost:8080
active_team: my-team
```

### CLI Environment Variables

| Variable | Description |
|---|---|
| `ALCOVE_SERVER` | Bridge server URL. Overrides the value in `config.yaml`. Overridden by `--server`. |
| `ALCOVE_USERNAME` | Username for Basic Auth. Overridden by `--username` flag. |
| `ALCOVE_PASSWORD` | Password for Basic Auth. Overridden by `--password` flag. |
| `HTTP_PROXY` | HTTP proxy URL for API requests |
| `HTTPS_PROXY` | HTTPS proxy URL for API requests (takes precedence over `HTTP_PROXY`) |
| `NO_PROXY` | Comma-separated list of hosts to exclude from proxy |
| `http_proxy` | Alternative lowercase version of `HTTP_PROXY` |
| `https_proxy` | Alternative lowercase version of `HTTPS_PROXY` |
| `no_proxy` | Alternative lowercase version of `NO_PROXY` |
| `XDG_CONFIG_HOME` | Base directory for config files. Defaults to `~/.config`. |

### Global Flags

| Flag | Description |
|---|---|
| `--server <url>` | Bridge server URL. Highest priority, overrides everything. |
| `--output <format>` | Output format: `json` or `table` (default: `table`). |
| `--proxy-url <url>` | HTTP/HTTPS proxy URL. Overrides environment variables. |
| `--no-proxy <hosts>` | Comma-separated list of hosts to exclude from proxy. Overrides `NO_PROXY` env var. |
| `-u, --username <user>` | Username for Basic Auth. Overrides `ALCOVE_USERNAME`. |
| `-p, --password <pass>` | Password for Basic Auth. Overrides `ALCOVE_PASSWORD`. |
| `--team <name>` | Team context for the request. Overrides `active_team` in config. |

### Server Resolution Order

The CLI resolves the Bridge URL in this order:

1. `--server` flag
2. `ALCOVE_SERVER` environment variable
3. `server` field in `~/.config/alcove/config.yaml`

---

## Teams

Teams are the universal ownership unit. Every resource (sessions, credentials,
security profiles, agent definitions, schedules, workflows, tools, agent repos)
belongs to a team. Every user belongs to one or more teams.

### X-Alcove-Team Header

All API requests include an `X-Alcove-Team` header to scope the request to a
team. The CLI sets this header automatically based on the `--team` flag or the
`active_team` field in `config.yaml`. The dashboard sets it based on the team
switcher selection.

```bash
curl -H "Authorization: Bearer $TOKEN" \
     -H "X-Alcove-Team: my-team-id" \
     http://localhost:8080/api/v1/sessions
```

### Teams API

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/v1/teams` | List teams for the authenticated user |
| `POST` | `/api/v1/teams` | Create a new team |
| `GET` | `/api/v1/teams/{id}` | Get team details |
| `PUT` | `/api/v1/teams/{id}` | Update team name |
| `DELETE` | `/api/v1/teams/{id}` | Delete a team |
| `POST` | `/api/v1/teams/{id}/members` | Add a member to a team |
| `DELETE` | `/api/v1/teams/{id}/members/{username}` | Remove a member from a team |

All team members have equal permissions. Any member can invite or remove others.
Admins can override team membership.

### Database Tables

Teams are stored in three tables:

| Table | Description |
|---|---|
| `teams` | Team ID, name, type (personal or shared), created/updated timestamps |
| `team_members` | Maps users to teams |
| `team_settings` | Per-team settings (e.g., agent repos) |

All resource tables use a `team_id` column (replacing the previous `owner`
column) to associate resources with teams.

---

## System LLM Setup

Alcove supports two LLM backends for the system LLM (used by AI-powered
features like the security profile builder). The system LLM is configured exclusively
in `alcove.yaml` or via environment variables -- it cannot be changed through
the dashboard or API. The dashboard shows a read-only status indicating
whether the system LLM is configured; edit `alcove.yaml` to change it.

### alcove.yaml Configuration

Add the system LLM settings to your `alcove.yaml`:

**Option A: Anthropic API**

```yaml
llm_provider: anthropic
llm_api_key: sk-ant-...
llm_model: claude-sonnet-4-20250514    # optional, defaults to claude-sonnet-4-20250514
```

**Option B: Google Vertex AI**

```yaml
llm_provider: google-vertex
llm_service_account_json: '{"type":"service_account","project_id":"my-project",...}'
llm_project: my-gcp-project-id
llm_region: us-east5                   # optional, defaults to us-east5
llm_model: claude-sonnet-4-20250514    # optional
```

### Environment Variable Overrides

Environment variables override `alcove.yaml` values:

| Variable | Description |
|---|---|
| `BRIDGE_LLM_PROVIDER` | LLM provider: `anthropic` or `google-vertex` |
| `BRIDGE_LLM_API_KEY` | Anthropic API key |
| `BRIDGE_LLM_MODEL` | Model name |
| `BRIDGE_LLM_SERVICE_ACCOUNT_JSON` | Google service account JSON (Vertex AI) |
| `BRIDGE_LLM_PROJECT` | GCP project ID (Vertex AI) |
| `BRIDGE_LLM_REGION` | GCP region (Vertex AI) |

The credential is injected into Gate as `GATE_LLM_TOKEN` at task launch time.
The key never enters the Skiff container.

## LLM Provider Setup

Alcove also supports configuring LLM providers for task execution via the
credentials API and dashboard. At least one provider must be configured for
Claude Code to function.

### Quick Start (Environment Variables)

For initial setup, you can set environment variables. Bridge auto-migrates
these into the credential store on first startup:

```bash
# Anthropic API (simplest)
export ANTHROPIC_API_KEY=sk-ant-...

# Google Vertex AI
export VERTEX_PROJECT=your-gcp-project-id
export VERTEX_API_KEY=your-vertex-api-key
```

After first startup, manage providers through the dashboard or API instead.

---

## Auth Backend Selection

The `AUTH_BACKEND` variable controls how user accounts are stored and
authenticated.

### `memory` (default)

Users are stored in memory. A default `admin` / `admin` account is created on
startup. Suitable for single-node or development deployments.

- Password hashes use argon2id encoding.

### `postgres`

Users are stored in PostgreSQL (the same Ledger database). Enables the user
management REST API at `/api/v1/users`.

- A default `admin` / `admin` account is created if no users exist in the
  database.
- Supports creating, listing, and deleting users via the API.
- Change the default password after first login via the dashboard.

### `rh-identity`

Users are authenticated via the `X-RH-Identity` header set by Red Hat's
Turnpike gateway. Intended for Red Hat internal deployments behind Turnpike.

- No login form, no passwords, no session tokens — identity comes from the
  trusted header.
- Users are auto-provisioned (JIT) on first request. Identity fields are
  extracted from the base64-decoded SAML identity: `username` (email),
  `external_id` (rhatUUID), `display_name` (givenName + surname).
- Users are stored in PostgreSQL (same `auth_users` table) without passwords.
- Bootstrap admins via `rh_identity_admins` in `alcove.yaml` or the
  `RH_IDENTITY_ADMINS` environment variable (comma-separated list of
  usernames/emails).
- After bootstrap, existing admins can promote or demote users from the
  dashboard.

Example `alcove.yaml`:

```yaml
auth_backend: rh-identity
rh_identity_admins: alice@redhat.com,bob@redhat.com
database_url: postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable
database_encryption_key: your-aes-256-key-here
```

---

## Kubernetes

On Kubernetes, provide `ALCOVE_DATABASE_ENCRYPTION_KEY` via a k8s Secret mounted as an
environment variable in the Bridge Deployment:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: alcove-database-encryption-key
type: Opaque
stringData:
  database-encryption-key: "your-random-32-byte-key-here"
---
# In the Bridge Deployment spec:
env:
  - name: ALCOVE_DATABASE_ENCRYPTION_KEY
    valueFrom:
      secretKeyRef:
        name: alcove-database-encryption-key
        key: database-encryption-key
```

---

## Agent Repos and Agent Definitions

Agent repos are git repositories containing YAML agent definitions in
`.alcove/tasks/*.yml`. They allow teams to define reusable, version-controlled
agents that appear in the dashboard.

Configure agent repos in the dashboard or via the API:

- **System-wide (admin):** `GET/PUT /api/v1/admin/settings/task-repos`
- **Per-user:** `GET/PUT /api/v1/user/settings/task-repos`

Bridge syncs agent repos automatically every 5 minutes (configurable via
`TASK_REPO_SYNC_INTERVAL`). Each YAML file defines an agent:

```yaml
name: run-tests
prompt: |
  Run the full test suite and fix any failures.
repo: https://github.com/org/myproject.git
provider: anthropic
model: claude-sonnet-4-20250514
timeout: 1800
budget_usd: 5.0
profiles:
  - read-only-github
tools:
  - github
plugins:
  - name: code-review
    source: claude-plugins-official
  - name: my-custom-plugin
    source: https://github.com/org/my-plugin.git
    ref: main
credentials:
  DEPLOY_TOKEN: production-deploy
  SLACK_WEBHOOK: slack-notifications
schedule: "0 2 * * *"
```

| Field       | Type     | Required | Description |
|-------------|----------|----------|-------------|
| `name`      | string   | yes      | Unique agent name |
| `prompt`    | string   | yes      | The agent instruction |
| `repo`      | string   | no       | Git repository URL to clone |
| `provider`  | string   | no       | LLM provider name |
| `model`     | string   | no       | Model override |
| `timeout`   | int      | no       | Timeout in seconds |
| `budget_usd`| float    | no       | Maximum spend |
| `profiles`  | string[] | no       | Security profile names to apply |
| `tools`     | string[] | no       | MCP tool names to enable |
| `plugins`   | PluginSpec[] | no   | Claude Code plugins to install (see [Plugins](#plugins)) |
| `credentials` | map[string]string | no | Environment variable names to credential provider mappings (see [Credentials](#credentials)) |
| `schedule`  | string   | no       | Cron expression for automatic execution |
| `labels`    | string[] | no       | GitHub issue/PR labels for event filtering (see below) |
| `users`     | string[] | no       | GitHub usernames for event filtering (see below) |

### Event Delivery Mode

Agent definitions with event triggers support two delivery modes:

- **`polling`** (default) — Alcove polls the GitHub Events API every 60 seconds for new events. Works in any environment including local development. No GitHub webhook configuration required.
- **`webhook`** — GitHub pushes events to Alcove's webhook endpoint (`/api/v1/webhooks/github`). Requires a publicly accessible URL and webhook secret configuration.

Example with polling mode:

```yaml
trigger:
  github:
    events: [issues]
    actions: [opened]
    repos: [owner/repo]
    delivery_mode: polling
```

Polling uses GitHub's conditional request support (ETags) to minimize API usage. On first poll, existing events are skipped to avoid a flood of retroactive session dispatches.

### Label-Based Trigger Filtering

The `labels` field provides a safety gate for event triggers. When specified,
an event is only dispatched if at least one of the listed labels is present on
the issue or pull request. This prevents unauthorized or unexpected issues from
triggering automated development tasks.

```yaml
name: auto-fix
prompt: |
  Investigate and fix the issue described above.
repo: https://github.com/org/myproject.git
trigger:
  github:
    events: [issues]
    actions: [opened, labeled]
    repos: [org/myproject]
    labels: [ready-for-dev]
```

If `labels` is omitted or empty, all matching events are dispatched regardless
of labels on the issue or PR.

### User-Based Trigger Filtering

The `users` field provides a safety gate for event triggers. When specified,
an event is only dispatched if the user who authored the comment or issue
matches at least one of the listed GitHub usernames (case-insensitive). This
prevents automated agents' own comments from re-triggering sessions and limits
session dispatch to trusted users.

```yaml
name: auto-fix
prompt: |
  Investigate and fix the issue described above.
repo: https://github.com/org/myproject.git
trigger:
  github:
    events: [issues, issue_comment]
    actions: [opened, created]
    repos: [org/myproject]
    labels: [ready-for-dev]
    users: [bmbouter]
```

If `users` is omitted or empty, all matching events are dispatched regardless
of the event author.

Agent definitions appear in the dashboard where users can run them directly or
view the source YAML. Starter templates are also available via
`GET /api/v1/task-templates`.

---

## YAML Security Profiles

Security profiles can also be defined in YAML files inside agent repos,
alongside agent definitions. Profile files live in `.alcove/security-profiles/*.yml`
(parallel to `.alcove/tasks/`) and are synced from the same registered agent repos.

### Format

```yaml
name: my-profile
display_name: My Profile
description: Description of what this profile grants
tools:
  github:
    rules:
      - repos: ["owner/repo"]
        operations: ["clone", "read_prs", "read_issues"]
```

### Fields

| Field          | Type   | Required | Description |
|----------------|--------|----------|-------------|
| `name`         | string | yes      | Unique profile identifier |
| `display_name` | string | no       | Human-readable name |
| `description`  | string | no       | Profile description |
| `tools`        | object | yes      | Map of tool name to rules (must contain at least one tool) |

Each tool entry contains a `rules` array using the same format as API-created
security profiles (see [API Reference](api-reference.md#security)).

### Behavior

- YAML security profiles are **read-only** in the dashboard and API. They
  cannot be modified or deleted through the UI.
- Profile names from YAML take precedence alongside user-created profiles.
- Agent definitions can reference YAML-defined profiles in their `profiles`
  field. If an agent definition references a profile name that does not exist
  (as a user-created or YAML profile), a sync error is reported.
- YAML profiles are synced on the same interval as agent definitions
  (configurable via `TASK_REPO_SYNC_INTERVAL`, default 5 minutes).

---

## Plugins

Agent definitions can declare Claude Code plugins to install at Skiff startup.
Plugins are installed before Claude Code runs, so all declared plugins are
available for the entire session.

```yaml
name: my-developer-agent
prompt: |
  Review the codebase and suggest improvements.
plugins:
  - name: code-review
    source: claude-plugins-official
  - name: gopls-lsp
    source: claude-plugins-official
  - name: my-custom-plugin
    source: https://github.com/org/my-plugin.git
    ref: main
```

### Plugin Sources

| Source | Description | Example |
|--------|-------------|---------|
| `claude-plugins-official` | Official Anthropic plugins | `code-review`, `gopls-lsp` |
| `marketplace` (or empty) | Claude Code plugin marketplace | Any published plugin |
| Git URL | Custom plugin from a git repo | `https://github.com/org/plugin.git` |

### PluginSpec Fields

| Field    | Type   | Required | Description |
|----------|--------|----------|-------------|
| `name`   | string | yes      | Plugin name |
| `source` | string | no       | Plugin source: `claude-plugins-official`, `marketplace`, a git URL, or empty (defaults to marketplace) |
| `ref`    | string | no       | Branch or tag for git-sourced plugins |

Marketplace and official plugins are installed via `claude plugin install`.
Git-sourced plugins are cloned and loaded via `--plugin-dir` flags passed to
Claude Code. The `ALCOVE_PLUGINS` environment variable is set on the Skiff
container with the JSON-serialized plugin list.

### Plugin Bundles

Bundles are pre-configured sets of plugins for common workflows. Reference a
bundle by setting `source: bundle`:

```yaml
plugins:
  - name: sdlc-go
    source: bundle
```

Available bundles:

| Bundle | Plugins Included | Use Case |
|--------|-----------------|----------|
| `sdlc-go` | code-review, gopls-lsp, commit-commands | Go development |
| `sdlc-python` | code-review, commit-commands | Python development |
| `sdlc-typescript` | code-review, commit-commands | TypeScript/JavaScript development |
| `content` | claude-md-management | Documentation and content creation |

Bundles can be combined with individual plugins:

```yaml
plugins:
  - name: sdlc-go
    source: bundle
  - name: my-custom-plugin
    source: https://github.com/org/plugin.git
```

Duplicate plugins are automatically deduplicated.

---

## Credentials

Agent definitions can reference stored credentials by name and have them injected as environment variables at dispatch time. This enables agents (especially executable agents) to access custom services using Alcove's encrypted credential store.

```yaml
name: Splunk Log Analyzer
executable:
  url: https://github.com/pulp/pulp-service/releases/download/v1/agent-splunk
  args: ["--model", "claude-opus-4-6"]
credentials:
  SPLUNK_TOKEN: splunk
  JIRA_TOKEN: jira
  VERTEX_SA_JSON: google-vertex
```

### Configuration

The `credentials` field maps environment variable names to credential provider names:

- **Key** (left side): Environment variable name that will be set in the Skiff container
- **Value** (right side): Provider name from the credential store

At dispatch time, Bridge looks up each credential by provider name and injects the real token into the Skiff environment. This works for both prompt-based (Claude Code) and executable agents.

### Creating Credentials

Create credentials via the dashboard or API:

```bash
curl -X POST /api/v1/credentials -d '{
  "name": "Splunk",
  "provider": "splunk",
  "auth_type": "api_key", 
  "credential": "my-splunk-bearer-token"
}'
```

The credential is encrypted and stored in the `provider_credentials` table. The `provider` field becomes the lookup key for agent definitions.

### Security Model

- Credentials are injected directly into the Skiff container's environment variables
- This follows the same security model as existing LLM credential injection
- Real credentials are never stored in YAML, git repositories, or logs
- For maximum security, prefer HTTP-based APIs through Gate's proxy mechanism when possible
- Direct injection is intended for custom services that don't use HTTP or have custom authentication

---

## Complete Environment Variable Example

```bash
# ── Infrastructure ────────────────────────────────────────────
export HAIL_URL=nats://localhost:4222
export LEDGER_DATABASE_URL=postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable
export BRIDGE_PORT=8080
export RUNTIME=podman
export AUTH_BACKEND=memory          # or postgres, rh-identity

# ── Security ──────────────────────────────────────────────────
export ALCOVE_DATABASE_ENCRYPTION_KEY=change-me-to-a-random-32-byte-string

# ── System LLM (choose one, or set in alcove.yaml) ──────────
# Option A: Anthropic API
export BRIDGE_LLM_PROVIDER=anthropic
export BRIDGE_LLM_API_KEY=sk-ant-...
# export BRIDGE_LLM_MODEL=claude-sonnet-4-20250514

# Option B: Google Vertex AI
# export BRIDGE_LLM_PROVIDER=google-vertex
# export BRIDGE_LLM_SERVICE_ACCOUNT_JSON='{"type":"service_account",...}'
# export BRIDGE_LLM_PROJECT=your-gcp-project-id
# export BRIDGE_LLM_REGION=us-east5

# ── LLM Provider (for task execution, choose one) ───────────
# Option A: Anthropic API
export ANTHROPIC_API_KEY=sk-ant-...
# export ANTHROPIC_MODEL=claude-sonnet-4-20250514

# Option B: Google Vertex AI
# export VERTEX_PROJECT=your-gcp-project-id
# export VERTEX_API_KEY=your-vertex-api-key
# export VERTEX_MODEL=claude-sonnet-4-20250514

# ── Networking ────────────────────────────────────────────────
# export ALCOVE_NETWORK=alcove-internal
# export ALCOVE_EXTERNAL_NETWORK=alcove-external

# ── Debug ─────────────────────────────────────────────────────
# export ALCOVE_DEBUG=true

# ── Service Credentials (for Gate proxy) ──────────────────────
# GITHUB_TOKEN and GITLAB_TOKEN are stored via the credential API
# and injected as dummy tokens into Skiff containers by Bridge.
# Gate swaps them for real PATs at proxy time.

# ── Dashboard ─────────────────────────────────────────────────
# export ALCOVE_WEB_DIR=web

```
