# Architecture Decisions

Consolidated from four specialist reviews: Security, Platform, Developer Experience,
and Integration architecture.

## Resolved Decisions

### 1. Gate: Sidecar per Skiff Pod (not shared service)

**Decision**: Deploy Gate as a sidecar container in each Skiff pod.

**Rationale** (Security):
- A shared Gate holds all credentials for all concurrent sessions in one process.
  A memory-disclosure vulnerability exposes everything. A sidecar loads only the
  credentials for its specific session.
- A compromised shared Gate affects all Skiff pods. A compromised sidecar affects
  only one session.
- 1:1 session-to-process mapping eliminates session confusion bugs (Skiff A's
  request evaluated against Skiff B's scope).
- At 3-20 concurrent workers, the resource cost is negligible (~20-50 MB RAM per
  sidecar).

**Impact**: Skiff pods are Kubernetes pods with two containers: `skiff` (Claude Code)
and `gate` (sidecar proxy). They share a network namespace, so `HTTP_PROXY=localhost:8443`
works without Service routing.

### 2. HTTPS Interception: Protocol-Level (no MITM CA)

**Decision**: Use protocol-level interception, not MITM TLS.

**Approach by tool type**:

| Tool | Interception Method |
|------|-------------------|
| Git (clone/push/fetch) | Git credential helper routed through Gate |
| LLM API calls | Custom base URL → Gate localhost → real provider endpoint |
| MCP servers (Phase 2) | Gate wraps MCP — Claude Code talks to proxy stubs |
| `gh` / `glab` CLI | HTTP_PROXY + CONNECT tunneling (domain-level) |
| `curl` / arbitrary HTTP | HTTP_PROXY + CONNECT tunneling (domain-level) |

**Phase 1 simplification**: Skip MCP entirely. Configure Claude Code with CLI-only
tools (`Bash, Edit, Read, Write, Grep, Glob`). All external access happens through
CLI tools routed via HTTP_PROXY. MCP gateway added in Phase 2.

**Rationale** (Security): MITM CA is the weakest option — the CA private key in
Gate becomes a high-value target, breaks cert pinning, is detectable by the agent,
and requires per-runtime trust store configuration (Python, Node, Java all differ).

### 3. LLM API Key Isolation: Gate Proxies LLM Calls

**Decision**: Skiff pods never hold LLM API keys. Gate sidecar proxies LLM API
calls and injects the real key at request time.

**How it works**: Claude Code is configured with `ANTHROPIC_BASE_URL=http://localhost:8443`
(pointing to the Gate sidecar). Claude Code sends API requests to Gate over plain
HTTP on localhost. Gate adds the real API key header and forwards to the actual
provider endpoint over TLS.

**Rationale**: The latency impact is negligible (~0.1ms for a localhost hop vs
2-30s for LLM calls). This prevents prompt injection attacks from exfiltrating
LLM credentials, which could be used for expensive unauthorized API usage.

### 4. Skiff Lifecycle: Kubernetes Jobs + `podman run --rm` + `docker run --rm`

**Decision**: Use Kubernetes Jobs on OpenShift, `podman run --rm` or `docker run --rm` on laptop/server.

| Environment | Primitive | Creation | Cleanup |
|---|---|---|---|
| Kubernetes/OpenShift | `batch/v1 Job` with `ttlSecondsAfterFinished: 300` | Bridge creates via k8s API | Automatic TTL |
| Podman (laptop) | `podman run --rm` | Bridge calls podman CLI | Automatic via `--rm` |
| Docker | `docker run --rm` | Bridge calls Docker CLI | Automatic via `--rm` |

**Rationale** (Platform): Jobs provide the exact semantic model needed — run one
session, exit, report success/failure. Exit code 0 = success, non-zero = failure.
`activeDeadlineSeconds` provides hard timeout for free. `ttlSecondsAfterFinished`
handles garbage collection.

**Warm pool optimization** (Phase 4): For lower latency, maintain 2-3 idle Skiff
pods as a Deployment with `restart: Always`. Each picks up one session, executes,
exits. The Deployment controller restarts it, giving a fresh pod. This is a
self-reprovisioning single-use worker pool.

### 5. Hail: NATS

**Decision**: NATS (core NATS in Phase 1, JetStream when persistence is needed).

**Rationale** (Platform):
- Single static binary, zero mandatory config. On laptop: `podman run nats:latest`.
- Subject-based messaging maps to Alcove's needs: `tasks.dispatch`,
  `tasks.<id>.status`, `tasks.<id>.cancel`.
- Native request-reply pattern (Bridge dispatches, Skiff acknowledges) without
  building correlation logic.
- JetStream adds persistence later without API changes.

**Security requirement** (Security): Authenticate and encrypt the bus. Use NATS
credentials + TLS. Sign session messages with HMAC so Skiff pods verify they came
from Bridge. Encrypt the Gate token in session messages.

### 6. Ledger: PostgreSQL Only (Phase 1)

**Decision**: PostgreSQL for everything. No S3 until transcripts regularly exceed ~1MB.

**Rationale** (Platform): A typical session transcript is 50-500KB JSON. Even at
100 sessions/day averaging 500KB, that's 50MB/day — trivial for PostgreSQL. On
a laptop, PostgreSQL in a container is simple: `podman run postgres:16`.

**Schema** (Integration):
```sql
CREATE TABLE sessions (
    id            UUID PRIMARY KEY,
    task_id       UUID NOT NULL,
    submitter     TEXT NOT NULL,
    prompt        TEXT NOT NULL,
    scope         JSONB NOT NULL,
    provider      TEXT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ,
    exit_code     INT,
    outcome       TEXT,     -- completed, timeout, cancelled, error
    transcript    JSONB,    -- full Claude Code stream-json output
    proxy_log     JSONB,    -- Gate request log (written independently by Gate)
    artifacts     JSONB,    -- PR URLs, commits, files
    parent_id     UUID REFERENCES sessions(id)  -- for follow-up chains
);
```

**Write model**: Skiff pods write transcripts directly to Ledger using
session-scoped append-only tokens. Ledger accepts writes as an append-only
stream — no updates or deletes to session records. Gate writes its proxy log
independently, providing a second audit stream that cannot be tampered with by
the Skiff pod.

### 7. Container Runtime Abstraction

**Decision**: Go `Runtime` interface with `KubeRuntime`, `PodmanRuntime`, and `DockerRuntime` backends.

```go
type Runtime interface {
    RunTask(ctx context.Context, task TaskSpec) (TaskHandle, error)
    CancelTask(ctx context.Context, handle TaskHandle) error
    EnsureService(ctx context.Context, svc ServiceSpec) error
    StopService(ctx context.Context, name string) error
    CreateVolume(ctx context.Context, name string) (string, error)
}
```

**Secrets**: k8s Secrets on OpenShift, environment variables (via `--env` /
`--env-file`) on podman. Both supported from Phase 1.

**Network isolation on podman**: Dual-network with `--internal` flag. Skiff
containers are attached only to `alcove-internal` (created with `--internal`,
no gateway, no internet route). Gate and infrastructure services are attached
to both `alcove-internal` and `alcove-external`. This provides kernel-level
isolation — Skiff cannot reach the internet even if `HTTP_PROXY` is bypassed.

**Network isolation on Docker**: Docker does not support the `--internal` flag
on network create, so Skiff containers have unrestricted network access. A
warning is logged at startup. Credential security is maintained (dummy tokens,
Gate injection), but adversarial prompt injection could bypass Gate. The Docker
runtime is intended for environments where Podman is unavailable (e.g., NAS
devices, some CI systems). Acceptable for personal/trusted deployments; use
Podman or Kubernetes for production/shared deployments.

### 8. Claude Code Invocation

**Decision**: The Skiff init process (`skiff-init`, a Go binary) is PID 1. Claude Code
runs as a child process.

```bash
claude \
  --print \
  --output-format stream-json \
  --dangerously-skip-permissions \
  --bare \
  --model "$CLAUDE_MODEL" \
  --max-budget-usd "$TASK_BUDGET" \
  --session-id "$TASK_UUID" \
  --no-session-persistence \
  "$PROMPT_TEXT"
```

**Key flags**:
- `--print` — non-interactive, exits when done
- `--output-format stream-json` — real-time NDJSON events for live streaming
- `--dangerously-skip-permissions` — no permission prompts (appropriate because
  the sandbox has no direct internet access)
- `--bare` — no hooks, plugins, keychain, CLAUDE.md discovery. Context provided
  explicitly.

### 9. Stopping Conditions

| Condition | Implementation |
|-----------|---------------|
| Normal exit | Claude Code exits 0; init process flushes transcript, exits |
| Hard timeout | `SIGTERM` after configured duration, `SIGKILL` after 10s grace |
| Manual cancel | Bridge sends cancel via NATS topic `tasks.<id>.cancel`; init sends `SIGTERM` |
| Heartbeat timeout | Init monitors stream-json stdout; 10 min silence triggers `SIGTERM` |

### 10. Session Transcript Delivery

**Decision**: Write-ahead log with periodic flush to database; dashboard uses polling.

1. Init process reads Claude Code's NDJSON stdout line-by-line
2. Each event is written to local WAL (`/tmp/alcove-transcript-<id>.jsonl`)
3. Events are flushed to the database every 5 seconds via HTTP POST
4. On exit, final reconciliation flushes any unsent events
5. Pod `terminationGracePeriodSeconds: 60` allows flush to complete
6. Dashboard polls `GET /api/v1/sessions/{id}/transcript` every 5 seconds
   (same approach as proxy log). Client-side streaming (EventSource and
   fetch+ReadableStream) was removed due to Akamai/Turnpike incompatibility.

### 11. Vertex AI Credentials

**Decision**: Service Account JSON mounted as k8s Secret (Phase 1), injected into
Gate sidecar (not Skiff container). Gate proxies LLM calls, adding auth.
Workload Identity Federation on OpenShift (Phase 2).

### 12. Git Workflow: Fresh Clone Per Session

**Decision**: Claude Code clones repos itself through Gate. `git clone --depth=1`
per session. No persistent volumes. No pre-cloning by the init process.

For repos >500MB (Phase 4): read-only git mirror maintained by Bridge, mounted
into Skiff pods. `git clone --reference /mnt/mirror --dissociate` copies objects
locally so the pod is self-contained.

### 13. Podman Deployment Topology

**Decision**: Dual podman networks, individual containers, Makefile-driven.

```
alcove-internal network (--internal, no internet gateway)
├── bridge (port 8080)   — controller + dashboard
├── hail (NATS)          — message bus
├── ledger (PostgreSQL)  — session storage
├── skiff-<task-id> (ephemeral, ONLY on this network — no internet access)
│   └── skiff container (Claude Code + init process)
└── gate-<task-id> (auth proxy + LLM proxy)

alcove-external network (normal, internet access)
├── bridge
├── hail
├── ledger
└── gate-<task-id>       — Gate bridges both networks for external access
```

`make dev-up` starts the infrastructure. Bridge dynamically creates Skiff pods on
the same network when tasks are dispatched.

On Docker, the same topology applies but without the `--internal` flag — all
containers share a single network with internet access. See Decision 7 for the
security implications.

### 14. Auth Model: Argon2id + Rate Limiting

**Decision**: Basic built-in auth with security hardening from day one.

| Control | Implementation |
|---------|---------------|
| Password hashing | argon2id (memory=64MB, iterations=3, parallelism=4) |
| Rate limiting | 5 failed attempts / 15 min → 30 min lockout |
| Sessions | Random tokens, HttpOnly + Secure + SameSite cookies, 8h expiry |
| CSRF | Synchronizer token pattern |
| TLS | Required (HTTPS only, no HTTP fallback) |
| Bootstrap | Default admin account (`admin`/`admin`), changeable in dashboard |
| SSO | OIDC integration in Phase 2 (not deferred to "roadmap") |

### 15. License

**Decision**: Apache-2.0 (CNCF/k8s ecosystem standard).

### 16. Language

**Decision**: Go. Single statically-linked binary for CLI and server components.
CLI and server share the same Go module and API types.

### 17. DNS in Skiff Pods

**Decision**: Remove DNS access entirely from Skiff pods (Phase 2 hardening).

Skiff pods only need to reach Gate (localhost sidecar), Hail, and Ledger.
Use `hostAliases` in the pod spec for Hail and Ledger IPs. Gate is on
localhost (shared network namespace). No DNS resolution needed.

This eliminates DNS exfiltration as an attack vector with zero infrastructure cost.

### 18. Secrets Management

**Decision**: k8s Secrets on OpenShift, environment variables on podman. Both
supported from Phase 1. No separate vault service. HashiCorp Vault or External
Secrets Operator integration deferred to Phase 5.

Bridge reads service credentials from k8s Secrets (or env vars) and injects them
into Gate sidecars at session creation time. Defense-in-depth within Bridge is
sufficient for Phase 1 (minimal RBAC, audit logging on all API endpoints).

### 19. Workflow Graph with Bounded Cycles

**Decision**: Workflows support a directed graph of steps with two types:
**agent steps** (Skiff pods running Claude Code) and **bridge steps**
(deterministic actions executed inline by Bridge).

**Bridge actions**: Three built-in actions move infrastructure concerns out of
LLM prompts and into reliable Bridge code:

| Action | Description |
|--------|-------------|
| `create-pr` | Creates a GitHub pull request from a branch |
| `await-ci` | Polls CI status on a PR until all checks complete |
| `merge-pr` | Merges a pull request |

Bridge actions take structured inputs and produce structured outputs that
downstream steps can reference via template variables
(`{{steps.<id>.outputs.<key>}}`).

**Dependencies**: Steps declare dependencies via boolean expressions
(`depends: "A.Succeeded && B.Succeeded"`) supporting `&&`, `||`, parentheses,
and `.Succeeded`/`.Failed` conditions. This replaces the older `needs` list
syntax (which is still supported for backward compatibility).

**Bounded cycles**: Steps can reference each other in cycles (e.g.,
review -> revision -> review). The `max_iterations` field prevents infinite
loops. When exhausted, the step status becomes `max_iterations_exceeded`.
Iteration tracking is stored in `workflow_run_steps` (migration
`028_workflow_graph_v2.sql`).

**Rationale**: LLMs are unreliable for infrastructure operations like creating
PRs, polling CI, and merging. Moving these to deterministic Bridge code
eliminates prompt engineering fragility and makes workflows auditable. Bounded
cycles enable practical review/revision patterns without risking infinite loops.


## CLI Design

### Phase 1 Commands

```
alcove run          Start a session (optionally --watch for live stream)
alcove list         List sessions (filterable by status, repo, date)
alcove logs         Stream or fetch session transcript / proxy log
alcove status       Show single session status
alcove cancel       Cancel a running session
alcove schedule     Create/list/enable/disable/delete scheduled sessions
alcove login        Authenticate to a Bridge instance
alcove config       Validate and show effective configuration
alcove version      Print client and server versions
```

**Design principles**:
- Session IDs are 8-char alphanumeric (human-friendly), full UUIDs stored internally
- Default to non-blocking (`alcove run` starts a session and returns; `--watch` for live)
- Every command supports `--output json` for machine-readable output
- Stderr for progress/spinners, stdout for results (enables piping)


## Configuration

Bridge is configured via environment variables (infrastructure settings) and
the dashboard/API (credentials, providers, system LLM, users). The default
admin account is `admin` / `admin`.

The CLI stores its Bridge URL in `~/.config/alcove/config.yaml` (set by
`alcove login`).


## Roadmap (Revised)

### Phase 1: Foundation
- Go monorepo with `cmd/bridge`, `cmd/gate`, `cmd/skiff-init`
- `KubeRuntime`, `PodmanRuntime`, and `DockerRuntime` backends
- Skiff pods as Jobs (k8s) / `podman run --rm` / `docker run --rm`
- Gate as sidecar with HTTP_PROXY + git credential helper + LLM API proxy
- NATS (core, no persistence) for Hail
- PostgreSQL for Ledger (append-only writes)
- Built-in auth (argon2id, rate limiting, CSRF)
- CLI: `run`, `list`, `logs`, `status`, `cancel`, `login`
- Dashboard: session list, new session, scope approval, live monitor, session review
- Vertex AI provider (key in Gate, not in Skiff)
- Manual scope configuration (no AI resolution)
- k8s Secrets + env vars for credentials
- `make dev-up` / `make dev-down` for podman

### Phase 2: Smart Scoping + Security Hardening
- AI-powered scope resolution (Gemini Flash via Vertex AI)
- Service catalog with risk levels
- Scope presets
- Scope approval UI in dashboard
- Gate MCP gateway (proxy stubs for MCP servers)
- OIDC/SSO integration
- DNS removal from Skiff pods (hostAliases for internal services)
- NATS authentication + TLS
- Ledger encryption at rest
- Token rotation (every 5 min within a session)
- Anthropic API provider
- `alcove schedule` command + cron scheduler in Bridge
- Scope escalation notifications (dashboard modal + CLI prompt)

### Phase 3: Human-in-the-Loop + Review
- Session review workflow (approve/reject/follow-up/rerun)
- Proxy log correlation with transcripts
- Session annotation sidebar in dashboard
- Follow-up session chaining (linked sessions)
- Claude Pro/Max account support
- Notification webhooks (Slack, email) for scheduled session approvals

### Phase 4: Dynamic Workers + Scale
- Custom Skiff images per-project (Containerfile generation)
- Warm pool (Deployment-based self-reprovisioning workers)
- Parallel session execution
- Resource limit configuration per session
- Git mirror volumes for large repos (>500MB)
- ~~nftables-based network isolation on podman~~ — **Done** (implemented via dual-network with `--internal` flag)

### Phase 5: Operator + Multi-Tenancy
- Kubernetes Operator for deploying Alcove
- Namespace-per-team isolation
- RBAC integration with OpenShift
- Quota management
- Federated session search across teams
- S3/MinIO for transcript storage (large sessions)
- HashiCorp Vault / External Secrets Operator integration


## Repository Layout

```
alcove/
├── cmd/
│   ├── bridge/         # Controller binary
│   ├── gate/           # Authorization proxy binary
│   └── skiff-init/     # Skiff init process binary
├── internal/
│   ├── runtime/        # KubeRuntime + PodmanRuntime + DockerRuntime
│   ├── hail/           # NATS client wrapper
│   ├── ledger/         # PostgreSQL client
│   ├── gate/           # Proxy logic, scope enforcement, token swap, LLM proxy
│   ├── bridge/         # Controller logic, API handlers, scheduler
│   └── auth/           # Authentication, session management
├── build/
│   ├── Containerfile.bridge
│   ├── Containerfile.gate
│   ├── Containerfile.skiff-base
│   └── Containerfile.skiff-example
├── deploy/
│   ├── k8s/            # Kubernetes manifests (Jobs, Deployments, Services, RBAC)
│   └── podman/         # Makefile targets, dev setup
├── web/                # Dashboard frontend
├── docs/
│   └── design/
├── go.mod
├── go.sum
├── Makefile
├── LICENSE             # Apache-2.0
└── README.md
```
