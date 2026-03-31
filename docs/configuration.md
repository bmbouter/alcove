# Configuration Reference

This document covers every configuration option for Alcove's three components
(Bridge, Gate, Skiff) and the CLI client.

## Config Hierarchy

Bridge configuration comes from three sources (highest to lowest priority):

1. **Environment variables** -- always take precedence over config file values
2. **Config file (`alcove.yaml`)** -- infrastructure settings that should not be in the UI/API
3. **Dashboard / API** -- credentials, providers, system LLM, users, security profiles

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
| `AUTH_BACKEND` | string | `memory` | Authentication backend. Must be `memory` or `postgres`. See [Auth Backend Selection](#auth-backend-selection). |
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
| `TASK_REPO_SYNC_INTERVAL` | string (duration) | `5m` | How often Bridge syncs YAML task definitions from registered task repos. Accepts Go duration syntax. |

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
| `config.yaml` | Stores the Bridge server URL. Created by `alcove login`. |
| `credentials` | Stores the JWT authentication token. Created by `alcove login`. |

### CLI Environment Variables

| Variable | Description |
|---|---|
| `ALCOVE_SERVER` | Bridge server URL. Overrides the value in `config.yaml`. Overridden by `--server`. |
| `XDG_CONFIG_HOME` | Base directory for config files. Defaults to `~/.config`. |

### Global Flags

| Flag | Description |
|---|---|
| `--server <url>` | Bridge server URL. Highest priority, overrides everything. |
| `--output <format>` | Output format: `json` or `table` (default: `table`). |

### Server Resolution Order

The CLI resolves the Bridge URL in this order:

1. `--server` flag
2. `ALCOVE_SERVER` environment variable
3. `server` field in `~/.config/alcove/config.yaml`

---

## LLM Provider Setup

Alcove supports two LLM backends. At least one must be configured for Claude
Code to function. Providers are configured via the dashboard Settings page or
the credentials API. They are derived from credentials stored in the database.

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

### Dashboard Setup

1. Log in to the dashboard at http://localhost:8080
2. Go to **Settings** and configure the system LLM provider and model
3. Go to **Credentials** to add or update LLM credentials

The credential is stored encrypted (AES-256-GCM) in PostgreSQL and injected
into Gate as `GATE_LLM_TOKEN` at task launch time. The key never enters the
Skiff container.

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

## Skill Repos

Skill repos are git repositories containing Claude Code plugins (skills and
agents) that extend what Claude Code can do inside Skiff containers. Each repo
should have a `.claude-plugin/plugin.json` file with skill and agent
definitions under `skills/` and `agents/` directories.

Configure skill repos in the dashboard under **Settings** or via the API:

- **System-wide (admin):** `GET/PUT /api/v1/admin/settings/skill-repos`
- **Per-user:** `GET/PUT /api/v1/user/settings/skill-repos`

At dispatch time, Bridge merges system-wide and per-user skill repos and passes
them to Skiff via the `ALCOVE_SKILL_REPOS` environment variable. Skiff clones
each repo and passes the directories to Claude Code as `--plugin-dir` flags.

You can also set `ALCOVE_SKILL_REPOS` as a Bridge environment variable to
provide a default list without using the database.

---

## Task Repos and Task Definitions

Task repos are git repositories containing YAML task definitions in
`.alcove/tasks/*.yml`. They allow teams to define reusable, version-controlled
tasks that appear in the dashboard.

Configure task repos in the dashboard or via the API:

- **System-wide (admin):** `GET/PUT /api/v1/admin/settings/task-repos`
- **Per-user:** `GET/PUT /api/v1/user/settings/task-repos`

Bridge syncs task repos automatically every 5 minutes (configurable via
`TASK_REPO_SYNC_INTERVAL`). Each YAML file defines a task:

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
schedule: "0 2 * * *"
```

| Field       | Type     | Required | Description |
|-------------|----------|----------|-------------|
| `name`      | string   | yes      | Unique task name |
| `prompt`    | string   | yes      | The task instruction |
| `repo`      | string   | no       | Git repository URL to clone |
| `provider`  | string   | no       | LLM provider name |
| `model`     | string   | no       | Model override |
| `timeout`   | int      | no       | Timeout in seconds |
| `budget_usd`| float    | no       | Maximum spend |
| `profiles`  | string[] | no       | Security profile names to apply |
| `tools`     | string[] | no       | MCP tool names to enable |
| `schedule`  | string   | no       | Cron expression for automatic execution |

Task definitions appear in the dashboard where users can run them directly or
view the source YAML. Starter templates are also available via
`GET /api/v1/task-templates`.

---

## Complete Environment Variable Example

```bash
# ── Infrastructure ────────────────────────────────────────────
export HAIL_URL=nats://localhost:4222
export LEDGER_DATABASE_URL=postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable
export BRIDGE_PORT=8080
export RUNTIME=podman
export AUTH_BACKEND=memory

# ── Security ──────────────────────────────────────────────────
export ALCOVE_DATABASE_ENCRYPTION_KEY=change-me-to-a-random-32-byte-string

# ── LLM Provider (choose one) ────────────────────────────────
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
