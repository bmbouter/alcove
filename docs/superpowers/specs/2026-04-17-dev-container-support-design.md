# Dev Container Support for Alcove

**Date:** 2026-04-17
**Status:** Approved
**Author:** Brian Bouterse + Claude

## Problem

Alcove's SDLC pipeline agents write code without a local feedback loop. The only
validation comes from CI — the agent pushes code, waits for GitHub Actions, and
if CI fails, a new agent session starts from scratch with failure logs. This
cycle is slow (8-15 minutes per iteration), expensive ($5-15 in LLM costs per
session), and typically requires 2-3 CI cycles before code passes.

When developers work locally with Claude Code, they have a tight inner loop:
edit code, run tests, see results, iterate. Alcove agents lack this capability.

## Solution

Add optional dev container support. A project-provided dev container runs
alongside the agent's Skiff container, connected by a shared volume and an
Alcove-injected HTTP execution shim. The agent edits code, calls the shim to
build and test, reads results, and iterates — all before pushing.

## Design Principles

1. **Alcove is unopinionated** — dev containers are optional. Teams compose
   their own SDLC pipelines. No dev container required.
2. **Project teams own the dev container** — Alcove orchestrates it, doesn't
   build it. Dev containers already exist (or should) for human developers.
3. **No custom config formats** — no devcontainer.json dependency, no
   Alcove-specific dev config spec. One field in the agent definition, everything
   else in the project's CLAUDE.md.
4. **CLAUDE.md is the contract** — all dev container usage instructions live
   there, serving both Alcove agents and local human development.
5. **Minimal requirements** — Alcove places no additional requirements above
   what a local developer would need from the dev container.

## Architecture

```
Bridge
  ├── creates shared volume
  ├── starts dev container (project image + injected shim)
  │     └── waits for GET /healthz → 200
  └── starts Skiff + Gate (existing flow)
        └── agent clones repo to /workspace (shared volume)
            ├── edits code
            ├── curl POST http://dev-container:9090/exec → build/test
            ├── reads results, iterates
            └── pushes when satisfied
```

Both Skiff and the dev container mount the shared volume at the same path
(`/workspace`). The agent doesn't know the volume exists — it clones and edits
files normally. The dev container sees the same files at the same path.

## Agent Definition

A single `dev_container` field with an `image` reference:

```yaml
name: Autonomous Developer
prompt: |
  You are a coding agent...
repo: bmbouter/alcove
dev_container:
  image: ghcr.io/bmbouter/alcove-dev:latest
```

No other configuration. How to use the dev environment is documented in the
project's CLAUDE.md.

## Execution Shim

A small Go binary (~200 lines) injected by Alcove at runtime. It runs inside the
dev container and accepts HTTP requests from the agent.

### Injection

Alcove injects the shim via init container / volume copy. The dev container's
entrypoint is overridden to start the shim alongside the project's original
services. On k8s, an init container copies the binary. On Podman/Docker, the
binary is volume-mounted from the host.

### API

Two endpoints:

#### `GET /healthz`

No authentication. Returns `200 OK` with `{"status":"ok"}` when the dev
container's services are ready. Used by Bridge for readiness gating before
starting Skiff.

#### `POST /exec`

Executes a shell command inside the dev container.

**Authentication:** `Authorization: Bearer <session-token>` — per-session token
injected as an environment variable in both Skiff and the shim at container
creation.

**Request:**

```json
{"cmd": "make test", "timeout": 300}
```

| Field     | Type   | Required | Description                                    |
|-----------|--------|----------|------------------------------------------------|
| `cmd`     | string | yes      | Passed to `sh -c`. No sanitization.            |
| `timeout` | int    | no       | Seconds. Default 60, server-side max 600.      |

**Response:** HTTP 200, `Content-Type: application/x-ndjson`, chunked transfer
encoding. One JSON object per line:

```
{"stream":"stdout","data":"=== RUN TestFoo\n"}
{"stream":"stderr","data":"warning: deprecated\n"}
{"stream":"stdout","data":"--- PASS: TestFoo (0.01s)\n"}
{"stream":"exit","code":0,"elapsed":4.2}
```

Stream types:
- `stdout` — standard output, interleaved in arrival order
- `stderr` — standard error, interleaved in arrival order
- `exit` — final line, contains `code` (int) and `elapsed` (float seconds)

HTTP status semantics:
- `200` — command executed. Check `code` in the `exit` line for success/failure.
- `400` — bad request (malformed JSON, missing `cmd`)
- `401` — invalid bearer token
- `500` — shim internal error

Timeout behavior: process group is killed, final line is
`{"stream":"exit","code":-1,"error":"timeout after 300s","elapsed":300.0}`.

Client disconnect: shim detects broken pipe and kills the process group.

Concurrency: commands serialized via mutex. Concurrent requests queue.

### Endpoints NOT Included

- `/cancel` — TCP connection close handles this (shim kills process group on
  broken pipe)
- `/info` — agent discovers capabilities via `/exec` or CLAUDE.md
- `dir` / `env` request fields — shell handles these (`cd /foo && DEBUG=1 make test`)

## Agent Interaction

The agent calls the shim via `curl` from its Bash tool. No MCP tool needed.

```bash
curl -s --max-time 310 -X POST http://dev-container:9090/exec \
  -H "Authorization: Bearer $DEV_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd": "make test", "timeout": 300}'
```

The shim URL and usage patterns are documented in the project's CLAUDE.md,
which serves both Alcove agents and local developers.

## Security

### Network Isolation

The dev container and Skiff share an isolated network segment. The dev
container can reach Skiff (and vice versa) but cannot reach Gate, NATS,
Ledger, or the internet. This prevents a
compromised dev container from:

- Talking to NATS (hijacking sessions, injecting messages)
- Talking to Ledger/PostgreSQL (reading session data)
- Scanning the internal network for other shims
- Bypassing Skiff's proxy-enforced network restrictions

### Threat Model

The dev container is treated as **fully compromised by definition**. The agent
executes arbitrary, LLM-generated code inside it. Security is enforced at the
container boundary, not through command sanitization.

### Mitigations

| Mitigation                | Purpose                                              |
|---------------------------|------------------------------------------------------|
| Network isolation         | Prevent lateral movement to infrastructure           |
| Per-session bearer token  | Authenticate shim requests                           |
| `nosymfollow` on volume   | Prevent symlink traversal attacks on shared volume   |
| Resource limits           | Prevent resource exhaustion (CPU, memory, PIDs)      |
| Process group kill        | Clean up on timeout or disconnect                    |
| Command audit logging     | Log cmd + exit code + duration to NATS (not stdout)  |
| Non-root execution        | Reduce privilege inside dev container                |

### Shared Volume

The shared volume is a bidirectional trust boundary. The agent writes files that
the dev container executes, and vice versa. Mitigations:

- Mount with `nosymfollow` to prevent symlink traversal
- Dev container should run as non-root
- No `setuid`/`setgid` binaries via `nosuid` mount option

## Lifecycle

### Session-Scoped

Each agent step gets a fresh dev container. Created and destroyed with Skiff.
No state persists across workflow steps.

**Rationale:** Session-scoped containers preserve Alcove's core invariant that
sessions are self-contained with no persistent state crossing boundaries. This
eliminates cross-step poisoning, simplifies lifecycle management, and requires
no new infrastructure (no GC, no orphan cleanup, no reconciliation).

**Startup cost mitigation:** Dev container images should be optimized for fast
startup. Run migrations on startup (correct by construction, no stale image
risk). Pre-bake dependencies into the image.

**Revisit threshold:** If measured startup consistently exceeds 90 seconds per
step in 4+ step workflows, reconsider workflow-scoped containers with fresh
container + persistent volume.

### Startup Sequence

1. Bridge creates shared volume
2. Bridge starts dev container (project image + injected shim)
3. Bridge polls `GET /healthz` until 200
4. Bridge starts Skiff + Gate (existing flow)
5. Agent clones repo to `/workspace` (the shared volume)
6. Agent edits code, calls shim to build/test, iterates
7. Agent pushes when satisfied

### Teardown

- Skiff exits → entire container group destroyed
- On k8s: Pod deletion cascades to all containers (dev container is a second
  container in the Skiff Pod, like Gate)
- On Podman/Docker: Bridge stops and removes dev container alongside Skiff and
  Gate (same `CancelTask` path)

## Resource Limits

### Phase 1: Hardcoded Defaults

Same pattern as Skiff and Gate today. Sensible defaults baked into the runtime
code:

| Resource | Request | Limit  |
|----------|---------|--------|
| CPU      | 100m    | 2      |
| Memory   | 512Mi   | 4Gi    |

On Podman/Docker: no resource limits initially (matching current Skiff behavior).

### Phase 2: Configurable

Add optional `resources` field to the agent definition:

```yaml
dev_container:
  image: ghcr.io/bmbouter/alcove-dev:latest
  resources:
    memory: 8Gi
    cpu: 4
```

### Phase 3: Admission Control

Namespace-level quotas, capacity checks, scheduling-aware admission.

## Code Flow

The shared volume is invisible infrastructure. The agent doesn't know it exists.

### How It Works

1. Bridge creates a volume and mounts it at `/workspace` in both Skiff and the
   dev container
2. Agent clones the repo to `/workspace` (its normal working directory)
3. Dev container sees the same files at `/workspace`
4. Agent makes code changes
5. Agent calls shim to build/test
6. Dev container executes commands against the code at `/workspace`
7. Agent reads results, iterates or pushes

### Project-Specific Patterns

How the dev container uses the code is project-specific, documented in CLAUDE.md:

| Stack          | Pattern                                                    |
|----------------|------------------------------------------------------------|
| Node           | `npm install && node server.js` — source used directly     |
| Python         | `pip install -e /workspace/mypackage` — editable install   |
| Go             | `cd /workspace && make build` — compile in dev container   |
| Hosted Pulp    | Generate patch from changes, apply via `patch-manage.sh`   |

The shim does not activate virtualenvs or source shell profiles. CLAUDE.md
wraps commands explicitly (e.g., `source /app/venv/bin/activate && pytest`).

## Runtime Integration

### Kubernetes

The dev container is a third container in the Skiff Pod (alongside Skiff and
Gate). This provides:

- Co-termination (Pod deletion cascades)
- Shared network namespace (localhost communication)
- Shared volume via `emptyDir`

The shim is injected via an init container that copies the binary to a shared
`emptyDir` volume.

### Podman

The dev container is a separate container on the same internal network as Skiff
and Gate. Bridge manages its lifecycle alongside the existing containers.
Consider `podman pod create` for network namespace sharing.

The shim binary is volume-mounted from the host.

### Docker

Same as Podman but without `--internal` network isolation (matching existing
Docker limitations for Skiff).

## Special Case: Alcove Developing Itself

When Alcove's SDLC pipeline develops Alcove itself, the dev container runs
Bridge + PostgreSQL + NATS. This covers API tests, unit tests, and integration
tests.

Container deployment (Skiff/Gate creation) is NOT tested in the dev container.
Bridge's `Runtime` interface enables mock/stub testing for container lifecycle.
Actual container deployment is validated in CI only.

This limitation is acceptable and mirrors how container orchestration platforms
(Kubernetes, Docker) test themselves.

## Multi-Runtime Considerations

| Concern              | k8s                          | Podman                        | Docker                  |
|----------------------|------------------------------|-------------------------------|-------------------------|
| Volume type          | `emptyDir`                   | Named volume                  | Named volume            |
| Network              | Pod-internal localhost       | `podman pod` or bridge net    | Bridge network          |
| Co-termination       | Pod deletion                 | Bridge cleanup                | Bridge cleanup          |
| UID alignment        | `fsGroup` in security ctx    | `--userns=keep-id`           | Match UIDs in images    |
| Shim injection       | Init container + emptyDir    | Host volume mount             | Host volume mount       |
| Network isolation    | NetworkPolicy                | `--internal` network          | None (accepted tradeoff)|

## Observability

- Bridge logs dev container start/stop events with session ID
- Shim logs command, exit code, and duration to NATS audit stream (NOT stdout/stderr)
- Dev container termination reason (OOMKilled, Error, Completed) captured in
  session record
- Dev container status visible in session detail view

## What This Does NOT Include

- Custom config formats or devcontainer.json dependency
- MCP tools for dev container interaction
- Workflow-scoped dev containers
- `/cancel`, `/info`, `dir`, `env` shim endpoints
- Namespace quotas or admission control
- Starter/template dev container images
- `podman exec` / `docker exec` for local development (local developers use
  their existing tools)
