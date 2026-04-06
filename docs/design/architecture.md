# Alcove Architecture

## Overview

Alcove is an OpenShift/Kubernetes-native platform for running sandboxed AI coding
agents. It provides filesystem isolation through ephemeral containers, network
isolation through an authorization proxy, and full auditability through session
recording. The platform is designed for teams that need AI-assisted development
with strong security guarantees and human-in-the-loop controls.

Claude Code is the supported agent runtime. Other agents may be supported in the
future, but the architecture is not designed around that goal today.


## Components

### Bridge — The Controller

The central coordinator. Runs as a long-lived Deployment. Provides:

- **Dashboard** — web UI for submitting tasks, reviewing session transcripts,
  configuring proxy scopes, and managing workers
- **REST API** — programmatic access to all dashboard functionality
- **Task Dispatcher** — interprets incoming prompts, determines authorization
  scope (see [Scope Resolution](#scope-resolution)), provisions proxy rules,
  and dispatches work to the message bus
- **Provider Registry** — manages LLM provider credentials (Google Vertex AI,
  Anthropic API keys, Claude Pro accounts) and injects them into worker pods.
  LLM API keys are never placed in Skiff pods — Bridge injects them into the
  Gate sidecar, which proxies LLM API calls and adds the real key at request time.
- **Session Browser** — queries the Ledger for session history, search, and
  audit review

Bridge never runs LLM prompts itself. It coordinates and observes.

#### Scheduled Tasks (Cron)

Bridge includes a built-in scheduler for recurring tasks. Users define schedules
via the dashboard or API:

```yaml
schedules:
  - name: nightly-dependency-audit
    cron: "0 2 * * *"                # 2 AM daily
    prompt: "Audit dependencies in pulp-service for known CVEs. Open a PR if updates are needed."
    repo: https://github.com/pulp/pulp-service
    scope_preset: security-audit     # predefined scope (clone + read + create_pr_draft)
    provider: vertex-ai
    timeout: 30m

  - name: weekly-docs-sync
    cron: "0 9 * * 1"                # Monday 9 AM
    prompt: "Check if any CLI flags were added in the last week and update the docs."
    repo: https://github.com/pulp/pulp-service
    scope_preset: docs-update
    provider: vertex-ai
    timeout: 45m
```

Bridge evaluates cron expressions and dispatches tasks to Hail at the scheduled
time. Scheduled tasks follow the same scope resolution, Gate authorization, and
Ledger recording as interactive tasks. The dashboard shows schedule history
(last run, next run, outcome) and allows enabling/disabling individual schedules.

Schedules can use either a `scope_preset` (a named, pre-approved scope from the
service catalog) or go through the normal scope resolution process. For
autonomous scheduled tasks, scope presets are recommended — they skip the
approval step because the scope was approved when the schedule was created.

### Skiff — The Workers

Ephemeral containers that execute Claude Code prompts. Each Skiff pod:

1. Starts from a clean, purpose-built container image (Claude Code pre-installed,
   project tooling included)
2. Connects to the message bus and waits for a task message
3. Receives a task (prompt text + configuration)
4. Executes `claude` with the prompt
5. Streams session output to the Ledger
6. Exits when the prompt concludes (or hits a timeout)
7. The container terminates, and Kubernetes reprovisions a fresh pod

**Filesystem sandbox**: because each Skiff pod starts clean and is destroyed after
every task, there is no persistent state that can be poisoned across sessions. The
container image is the only input; the session transcript is the only output.

**Stopping conditions** (in priority order):

1. Claude Code exits normally (prompt complete)
2. Configurable hard timeout (default: 60 minutes, set per-task by Bridge)
3. Manual cancellation via Bridge dashboard/API
4. Heartbeat timeout — if Claude Code stops producing output for N minutes
   (default: 10), the pod is terminated

The Skiff image is built with:
- Claude Code CLI
- Project-specific tooling (language runtimes, build tools, linters)
- A thin init process that handles message bus connection, session streaming,
  and timeout enforcement
- Git (for cloning repos at task start)
- `gh` and `glab` CLIs (for GitHub/GitLab interaction via Gate's SCM proxy)
- A git credential helper that routes authentication through Gate

### Gate — The Authorization Proxy

A network-level gateway that controls all external access from Skiff pods.
Deployed as a **sidecar container** in each Skiff pod (sharing the network
namespace). Every outbound connection from a Skiff pod routes through its
Gate sidecar. Gate provides:

- **Operation-level authorization** — not just "can access GitHub" but "can create
  a PR on repo X but not merge it." Scopes are defined per-task by Bridge.
- **Token replacement** — Skiff pods never hold real credentials. They present a
  session-scoped opaque token. Gate maps this token to actual credentials
  (GitHub PAT, GitLab token, Jira API key, etc.) at request time.
- **LLM API proxying** — Skiff pods also lack LLM API keys. Claude Code is
  configured with a custom base URL pointing to the Gate sidecar (`ANTHROPIC_BASE_URL=http://localhost:8443`).
  Gate injects the real API key and forwards to the provider. This prevents
  prompt injection attacks from exfiltrating LLM credentials.
- **SCM API proxying** — Gate exposes `/github/` and `/gitlab/` reverse-proxy
  endpoints. `gh` and `glab` CLIs inside Skiff are configured via
  `GITHUB_API_URL` and `GITLAB_API_URL` to point at these local endpoints.
  Gate performs operation-level scope enforcement on every request, injects
  real SCM credentials, and forwards to the upstream API.
- **CLI and MCP coverage** — Gate intercepts both MCP tool calls and CLI-initiated
  network requests (e.g., `gh pr create`, `git push`, `curl`). This is achieved
  by running Gate as an HTTP/HTTPS proxy (via `HTTP_PROXY`/`HTTPS_PROXY` env vars
  in Skiff pods) combined with MCP server wrapping.
- **Request logging** — every proxied request is logged with timestamp, operation
  type, target service, authorization decision (allow/deny), and response status.
  Logs are forwarded to the Ledger.

**Prompt injection defense**: because neither real service tokens nor LLM API keys
enter the Skiff pod, a prompt injection attack that exfiltrates environment
variables or config files gets only the opaque session token, which is:
- Scoped to a single task's authorized operations
- Short-lived (expires when the task ends)
- Revocable by Bridge at any time

**HTTPS interception approach**: protocol-level, not MITM TLS.

| Tool | Interception Method |
|------|-------------------|
| Git (clone/push/fetch) | Git credential helper routed through Gate |
| MCP servers (Phase 2) | Gate wraps MCP — Claude Code talks to proxy stubs |
| `gh` / `glab` CLI | `GITHUB_API_URL` / `GITLAB_API_URL` pointing to Gate's `/github/` and `/gitlab/` HTTP endpoints (operation-level enforcement) |
| `curl` / arbitrary HTTP | HTTP_PROXY + CONNECT tunneling (domain-level) |
| LLM API calls | Custom base URL → Gate → real provider endpoint |

### Ledger — Session Storage

A persistent store for complete session records. Each task execution produces a
session record containing:

- **Task metadata** — prompt text, submitter, timestamp, authorization scope,
  timeout settings, provider used
- **Claude transcript** — full input/output record from Claude Code (the
  `--output-format stream-json` session dump)
- **Proxy log** — every Gate request/response for this session (written
  independently by Gate, not by the Skiff pod)
- **Outcome** — exit code, duration, whether timeout/cancellation occurred
- **Artifacts** — references to any PRs/MRs created, commits pushed, files changed

Ledger serves two purposes:

1. **Audit trail** — complete, tamper-evident record of what the AI did, what it
   was authorized to do, and what external calls it made. The transcript (from
   the Skiff) and the proxy log (from Gate) provide two independent audit streams.
2. **Human-in-the-loop review** — Bridge's dashboard queries Ledger to display
   session history for review, approval, or follow-up

Storage backend: PostgreSQL. Skiff pods write transcripts directly with
session-scoped append-only tokens. Ledger accepts writes as an append-only
stream — no updates or deletes to session records.

### Hail — The Message Bus

Connects Bridge to Skiff pods. Carries:

- **Task messages** (Bridge → Skiff) — prompt text, repo URL, branch, Gate token,
  timeout, provider config
- **Status updates** (Skiff → Bridge) — heartbeats, progress, completion
- **Control messages** (Bridge → Skiff) — cancellation signals

Implementation: **NATS** (core NATS in Phase 1, JetStream when persistence is
needed). Subject-based messaging maps to Alcove's needs: `tasks.dispatch`,
`tasks.<id>.status`, `tasks.<id>.cancel`.


## Scope Resolution

When a user submits a task, Bridge must determine what external services the Skiff
pod should be authorized to access and what operations are permitted. This is
called **scope resolution**.

### Process

1. User submits a prompt (e.g., "Fix the auth bug in pulp-service and open a PR")
2. Bridge's scope resolver analyzes the prompt using an LLM call (fast, cheap model)
   against a catalog of known services and their available operation scopes
3. The resolver produces a proposed scope:
   ```yaml
   services:
     github:
       repos: ["pulp/pulp-service"]
       operations: [clone, read_issues, create_pr_draft]
     # jira not included — prompt doesn't mention it
   ```
4. **In supervised mode** (default): the proposed scope is shown to the user in
   the dashboard for approval/modification before the task is dispatched
5. **In autonomous mode** (opt-in): the proposed scope is applied automatically,
   but logged for audit

### Service Catalog

Bridge maintains a catalog of known services and their operation scopes:

```yaml
services:
  github:
    operations:
      - clone            # git clone (read-only)
      - read_issues      # GET issues, comments
      - read_prs         # GET pull requests
      - create_pr_draft  # POST pull request (draft only)
      - create_pr        # POST pull request (ready for review)
      - merge_pr         # PUT merge pull request
      - push_branch      # git push to non-default branch
      - push_main        # git push to default branch (dangerous)
    default_scope: [clone, read_issues, read_prs, create_pr_draft]

  gitlab:
    operations:
      - clone
      - read_issues
      - read_mrs
      - create_mr_draft
      - create_mr
      - merge_mr
      - push_branch
    default_scope: [clone, read_issues, read_mrs, create_mr_draft]

  jira:
    operations:
      - read_issues
      - create_issue
      - edit_issue
      - transition_issue
      - add_comment
    default_scope: [read_issues, add_comment]
```

### Scope Escalation

If Claude Code attempts an operation that is not in the current scope, Gate
blocks the request and returns a structured error. The Skiff pod can relay this
to Bridge, which can:

1. **Notify the user** — "Claude wants to merge PR #42. Approve?" (human-in-the-loop)
2. **Auto-deny** — log and continue (default for dangerous operations)
3. **Auto-approve** — for pre-configured safe escalation paths


## Deployment Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  OpenShift Namespace: alcove                                        │
│                                                                     │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────────────┐  │
│  │  Bridge       │    │  Hail        │    │  Ledger              │  │
│  │  (controller) │◄──►│  (NATS)      │    │  (PostgreSQL)        │  │
│  │  :8080        │    │              │    │                      │  │
│  │  Dashboard +  │    └──────┬───────┘    └──────────▲───────────┘  │
│  │  API          │           │                       │              │
│  └──────┬────────┘           │                       │              │
│         │                    │                       │              │
│         │ scope + creds      │ task messages          │ transcripts  │
│         ▼                    ▼                       │              │
│  ┌──────────────────────────────────────┐            │              │
│  │  Skiff Pod (ephemeral)               │            │              │
│  │  ┌────────────┐  ┌────────────────┐  │            │              │
│  │  │  skiff     │  │  gate (sidecar)│──┼────────────┘              │
│  │  │  Claude    │──│  token swap    │  │                           │
│  │  │  Code +    │  │  LLM proxy     │  │                           │
│  │  │  init proc │  │  op authz      │──┼──► External Services      │
│  │  │            │  │  request log   │  │   (GitHub, GitLab, etc.)  │
│  │  └────────────┘  └────────────────┘  │                           │
│  └──────────────────────────────────────┘                           │
└─────────────────────────────────────────────────────────────────────┘
```

### Network Isolation

A core security requirement: **100% of Skiff pod traffic to non-Alcove services
must flow through Gate.** No direct connections to the internet or external APIs
are permitted. This is achievable and enforceable on OpenShift.

#### How It Works

OpenShift uses OVN-Kubernetes as its CNI plugin, which fully enforces
NetworkPolicy at the kernel level (OVS/eBPF). When an egress NetworkPolicy is
applied to a pod, **all egress traffic not matching an allow rule is dropped** —
this is not advisory, it is kernel-enforced packet filtering. There is no way
for a process inside the Skiff pod to bypass it regardless of what the LLM
attempts.

#### Policy Definition

```yaml
# Skiff pods can ONLY talk to Hail, Ledger, and cluster DNS.
# Gate is a sidecar (localhost), so no explicit rule is needed.
# All other egress is dropped at the kernel level.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: skiff-isolation
  namespace: alcove
spec:
  podSelector:
    matchLabels:
      alcove.dev/role: skiff
  policyTypes: [Egress]
  egress:
    # Allow DNS resolution (required for service discovery within the cluster)
    - to:
        - namespaceSelector: {}
          podSelector:
            matchLabels:
              dns.operator.openshift.io/daemonset-dns: default
      ports:
        - protocol: UDP
          port: 5353
        - protocol: TCP
          port: 5353
    # Allow traffic to Alcove infrastructure services only
    - to:
        - podSelector:
            matchLabels:
              alcove.dev/role: hail
        - podSelector:
            matchLabels:
              alcove.dev/role: ledger
---
# The Gate sidecar (inside the Skiff pod) is the ONLY path to external services.
# Since Gate shares the pod's network namespace, its egress is governed by the
# pod-level policy. We need a separate policy that allows the Skiff pod to
# reach external IPs — but ONLY the Gate container uses this path.
# This is enforced by HTTP_PROXY configuration in the skiff container.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: skiff-gate-egress
  namespace: alcove
spec:
  podSelector:
    matchLabels:
      alcove.dev/role: skiff
  policyTypes: [Egress]
  egress:
    # Gate sidecar needs to reach external services
    - ports:
        - protocol: TCP
          port: 443
        - protocol: TCP
          port: 80
```

#### What This Guarantees

| Traffic | Allowed? | Enforced by |
|---------|----------|-------------|
| Skiff → Gate (localhost sidecar) | Yes | Shared network namespace |
| Skiff → Hail | Yes | NetworkPolicy egress allow |
| Skiff → Ledger | Yes | NetworkPolicy egress allow |
| Skiff → cluster DNS | Yes | NetworkPolicy egress allow (UDP/TCP 5353) |
| Gate sidecar → github.com:443 | Yes | NetworkPolicy port 443 allow |
| Skiff container → github.com (direct) | Blocked by HTTP_PROXY | Gate handles all external |

#### Why This Is Airtight on OpenShift

1. **OVN-Kubernetes enforcement is mandatory** — unlike some CNI plugins where
   NetworkPolicy is optional, OVN-Kubernetes always enforces it. There is no
   "permissive mode."
2. **Pod-level enforcement** — policies are applied per-pod via OVS flow rules.
   Even if a Skiff pod runs as root inside the container, it cannot modify the
   network rules because they are enforced by the node's kernel, outside the
   container's network namespace.
3. **No privileged escalation** — Skiff pods run without privileged SCCs, so they
   cannot manipulate iptables, create raw sockets, or modify network interfaces.
4. **Egress default-deny** — when a NetworkPolicy with `policyTypes: [Egress]`
   is applied to a pod, any egress not explicitly allowed is dropped. This is
   the Kubernetes spec behavior, not an OpenShift extension.

#### Podman (Laptop) Network Isolation

On podman, NetworkPolicy does not exist. Instead, network isolation is enforced
via a dual-network architecture using podman's `--internal` flag:

- **`alcove-internal`** — created with `--internal`, which means it has no
  gateway and no route to the internet. Skiff containers are attached only to
  this network. Even if a prompt injection bypasses the HTTP_PROXY settings,
  Skiff has no route to external hosts.
- **`alcove-external`** — a normal podman network with internet access. Gate
  sidecars and infrastructure services (Bridge, Hail, Ledger) are attached to
  both networks. Gate can reach external services; Skiff cannot.

This provides kernel-level network isolation on podman, comparable to
NetworkPolicy on OpenShift. See
[podman-network-isolation.md](podman-network-isolation.md) for full details.

### LLM Provider Configuration

Bridge manages provider credentials and injects them into Gate sidecars at task
time. Skiff containers never hold LLM API keys.

| Provider | Credential | Gate Behavior |
|----------|-----------|---------------|
| Google Vertex AI | Service account JSON + project ID | Gate proxies to Vertex AI endpoint, injects OAuth token |
| Anthropic API | API key | Gate proxies to api.anthropic.com, injects API key header |
| Claude Pro (Max) | OAuth refresh token | Gate proxies to Claude API, injects auth token |


## Roadmap

### Phase 1: Foundation

- Bridge with basic dashboard (submit prompt, view tasks)
- Bridge REST API
- Skiff pods as k8s Jobs / `podman run --rm`
- Gate as sidecar with HTTP_PROXY + git credential helper + LLM API proxy
- Hail via NATS (core, no persistence)
- Ledger via PostgreSQL (append-only session records)
- Built-in auth (argon2id, rate limiting, CSRF)
- Manual scope configuration (no AI scope resolution)
- Vertex AI provider support
- CLI: `run`, `list`, `logs`, `status`, `cancel`, `login`
- `make dev-up` / `make dev-down` for podman

### Phase 2: Smart Scoping + Security Hardening

- AI-powered scope resolution (Gemini Flash via Vertex AI)
- Service catalog with operation-level granularity and risk levels
- Scope presets
- Scope approval UI in dashboard
- Gate MCP gateway (proxy stubs for MCP servers)
- OIDC/SSO integration
- DNS removal from Skiff pods (hostAliases for internal services)
- NATS authentication + TLS
- Ledger encryption at rest
- Token rotation (every 5 min within a task)
- Anthropic API provider support
- `alcove schedule` command + cron scheduler in Bridge

### Phase 3: Human-in-the-Loop + Review

- Scope escalation notifications (Gate block → Bridge notification → user approval)
- Task review workflow (approve/reject/follow-up/rerun)
- Proxy log correlation with session transcripts in dashboard
- Follow-up task chaining (linked sessions)
- Claude Pro/Max account support
- Notification webhooks (Slack, email) for scheduled task approvals

### Phase 4: Dynamic Workers + Scale

- Custom Skiff images per-project (Containerfile generation)
- Warm pool (Deployment-based self-reprovisioning workers)
- Parallel task execution
- Resource limit configuration per task
- Git mirror volumes for large repos (>500MB)
- ~~nftables-based network isolation on podman~~ — **Done** (implemented via dual-network with `--internal` flag)

### Phase 5: Operator + Multi-Tenancy

- Kubernetes Operator for deploying Alcove
- Namespace-per-team isolation
- RBAC integration with OpenShift
- Quota management
- Federated session search across teams
- S3/MinIO for transcript storage (large sessions)


## Component Summary

| Component | Name | k8s Resource | Long-lived | Purpose |
|-----------|------|-------------|------------|---------|
| Controller | **Bridge** | Deployment | Yes | Coordination, dashboard, API, scheduler |
| Worker | **Skiff** | Job / `podman run --rm` | No (ephemeral) | Execute Claude Code prompts |
| Auth Proxy | **Gate** | Sidecar in Skiff pod | No (per-task) | Network sandbox, token swap, LLM proxy |
| Message Bus | **Hail** | Deployment (NATS) | Yes | Task dispatch, status updates |
| Session Store | **Ledger** | Deployment + PVC | Yes | Audit trail, session history |
