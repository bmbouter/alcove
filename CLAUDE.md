# Alcove — Sandboxed AI Coding Agents on OpenShift/Kubernetes

## What This Is

Alcove runs AI coding agents (Claude Code) in ephemeral, network-isolated
containers. Each session gets a fresh container, a scoped authorization proxy, and
a complete session transcript. No persistent state crosses session boundaries.

**Language:** Go 1.25 | **License:** Apache-2.0

## Components

| Name | Role | Binary | k8s Resource |
|------|------|--------|-------------|
| **Bridge** | Controller, REST API, dashboard, scheduler, agent repo syncer | `cmd/bridge` | Deployment |
| **Skiff** | Ephemeral Claude Code worker | `cmd/skiff-init` | Job / `podman run --rm` / `docker run --rm` |
| **Gate** | Auth proxy sidecar (token swap, LLM proxy, SCM proxy, scope enforcement) | `cmd/gate` | Sidecar in Skiff pod |
| **Shim** | Execution sidecar injected into dev containers (`GET /healthz`, `POST /exec` with NDJSON streaming) | `cmd/shim` | Sidecar in dev container |
| **Hail** | Message bus (NATS) | external | Deployment |
| **Ledger** | Session store (PostgreSQL) | external | Deployment + PVC |

## Architecture

```
Bridge → Hail (NATS) → Skiff Pod [skiff container + gate sidecar] → Gate → External Services
                                ↕ /workspace volume (optional)              → Ledger (PostgreSQL)
                        Dev Container [project image + shim]
```

- Skiff pods are ephemeral: one session, one container, then destroyed
- Gate is a sidecar (shares network namespace with Skiff)
- Gate proxies ALL external traffic including LLM API calls (Skiff has no real credentials)
- Optional dev container runs alongside Skiff with a shared `/workspace` volume, enabling agents to build/test code in project-specific environments; the shim binary is baked into the dev container image via s6-overlay (built with `make build-dev`)
- On OpenShift, a static `alcove-allow-internal` NetworkPolicy restricts egress (per-task NetworkPolicy is disabled due to OVN-Kubernetes DNS resolution issues); dual-network isolation (`--internal` flag) on podman; no network isolation on Docker (see Key Decisions)

## Design Documents

Read these for full context:

1. `docs/design/implementation-status.md` — **START HERE** — current state, what works, what's next
2. `docs/design/architecture.md` — component design, deployment diagrams, network isolation, roadmap
3. `docs/design/architecture-decisions.md` — 22 resolved decisions, CLI design, config format, repo layout
4. `docs/design/problem-statement.md` — why ephemeral agents
5. `docs/design/credential-management.md` — credential storage, encryption, OAuth2 token flow
6. `docs/design/auth-backends.md` — auth backend design (memory, postgres, rh-identity)
7. `docs/design/gate-scm-authorization.md` — SCM proxy endpoints, operation taxonomy, security model

## Quick Commands

```bash
# Primary dev workflow — hot-reload with full session dispatch
make watch                    # Builds images, starts NATS+PostgreSQL, runs Bridge via Air
                              # Save a .go file → Air rebuilds → Bridge restarts → sessions work
make down                     # Stop everything (Bridge + NATS + PostgreSQL)

# First-time setup or database wipe (then switch to make watch)
make up                       # Build binaries + images, start containerized Bridge + infra
                              # Requires follow-up curl commands to seed credentials (see dev-up skill)

# Build
make build                    # Build all Go binaries to bin/
make build-images             # Build container images with podman (smart rebuild via stamps)
make build-tooling            # Build heavy skiff-tooling base image (only when tools change)
make test                     # Run tests

# Infrastructure
make dev-infra                # Start only NATS + PostgreSQL on podman
make dev-up                   # Start full containerized environment
make dev-down                 # Stop everything
make dev-reset                # Stop + remove volumes (clean slate)

# Run Bridge locally with Docker (after infrastructure setup)
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=docker \
./bin/bridge
```

## Code Conventions

- Standard Go project layout (`cmd/`, `internal/`)
- `net/http` for HTTP servers (no frameworks)
- `github.com/spf13/cobra` for CLI
- `github.com/nats-io/nats.go` for NATS
- `github.com/jackc/pgx/v5` for PostgreSQL
- Containerfiles use multi-stage builds (golang:1.25 → ubi9)
- All container images use `localhost/alcove-<component>:dev` tags locally

## Key Decisions

- **Teams are the ownership unit** — every resource belongs to a team; every user belongs to one or more teams; personal team auto-created on signup; `X-Alcove-Team` header scopes all API requests
- **Gate is a sidecar** per Skiff pod (not shared service) — credential isolation
- **No MITM TLS** — protocol-level interception (HTTP_PROXY, git credential helpers)
- **LLM keys never enter Skiff** — Gate proxies LLM API calls, injects keys; Gate also translates Anthropic API format to Vertex AI format when using Vertex AI (`GATE_VERTEX_PROJECT`, `GATE_VERTEX_REGION`)
- **Fresh git clone per session** — `git clone --depth=1`, no persistent volumes
- **NATS for messaging** — status updates and cancellation only (session config via env vars)
- **PostgreSQL only** for Ledger (no S3 in Phase 1)
- **Podman, Docker + k8s** triple runtime via `Runtime` interface in `internal/runtime/` — Docker is for environments where Podman is unavailable (e.g., NAS devices, some CI systems); network isolation is reduced with Docker (no `--internal` flag support), so Skiff containers can reach the internet directly; credential security is maintained (dummy tokens, Gate injection), but adversarial prompt injection could bypass Gate; acceptable for personal/trusted deployments, use Podman or Kubernetes for production/shared deployments
- **Credential management via Bridge** — Bridge pre-fetches OAuth2 tokens, Gate receives only short-lived tokens
- **Three auth backends** — `AUTH_BACKEND=memory` (default), `postgres`, or `rh-identity` (trusted `X-RH-Identity` header from Red Hat Turnpike, JIT user provisioning, no passwords)
- **SCM and tool APIs proxied through Gate** — `/github/`, `/gitlab/`, and `/jira/` endpoints with dummy tokens, operation-level scope enforcement, real credentials never enter Skiff
- **Custom migration runner** — embedded SQL files, advisory locking, no external dependencies
- **Per-item catalog granularity** — catalog has a two-level hierarchy: sources (git repos, unit of distribution) and items (plugins, agents, LSPs, MCPs); catalog items are seeded from embedded data at compile time (no runtime cloning of catalog source repos); teams toggle individual items, not whole sources; enabled agents are referenced in workflow steps as `source/item` slugs; workflow definitions are validated at sync time for unknown/disabled agent references
- **Workflow graph with bounded cycles** — workflows support agent steps (Skiff pods) and bridge steps (deterministic `create-pr`/`await-ci`/`merge-pr` actions); `depends` expressions with `&&`/`||`; `max_iterations` prevents infinite loops in review/revision cycles
- **YAML is the single source of truth for schedules, security profiles, and tools** — no API-based creation, update, or deletion; schedules are defined via `schedule:` in `.alcove/tasks/*.yml`; security profiles in `.alcove/security-profiles/*.yml`; tools come from catalog or builtin definitions; the API provides read-only access to synced data
- **`alcove.yaml` for infrastructure settings** — config file search order: `ALCOVE_CONFIG_FILE` env var → `./alcove.yaml` → `/etc/alcove/alcove.yaml`; env vars always override; `database_encryption_key` is required (Bridge refuses to start without it); `make up` auto-generates the file for local dev; file is gitignored
- **`.dev-credentials.yaml` for dev credentials** — single source of truth for local dev LLM provider and GitHub PAT; copy `.dev-credentials.yaml.example`, fill in values; `make dev-config` (run by `make up`) merges LLM settings into `alcove.yaml`; the dev-up process reads it to create API credentials in the database; file is gitignored
- **Dev containers are optional sidecars** — agent definitions can declare `dev_container.image` to run a project-provided container alongside Skiff; dev container images are built with s6-overlay and the shim binary baked in (`make build-dev` builds the base image from `build/Containerfile.dev`); `Containerfile.dev` is an all-in-one dev container image that includes PostgreSQL 16, NATS, Go 1.25, the shim binary, and s6-overlay for process supervision; s6 manages PostgreSQL, NATS, and the shim as supervised services with proper dependencies; Podman creates a shared workspace volume at `/workspace` and mounts it in both containers; the shim provides bearer-auth-protected `POST /exec` for remote command execution with NDJSON streaming; `dev_container.network_access` controls network access (`internal` default, `external` joins both networks on Podman); on Kubernetes, the dev container runs as a native sidecar with emptyDir workspace volume (`DEV_CONTAINER_HOST=localhost:9090`); Docker rejects dev containers with a clear error; `--security-opt label=disable` handles SELinux compatibility on Podman; see architecture decision #20 in `docs/design/architecture-decisions.md` for the full design
- **Multi-repo support** — agent definitions use `repos:` (a list of `RepoSpec` with `name`, `url`, `ref` fields) instead of a single `repo:` string; Skiff receives a `REPOS` JSON env var and clones each repo into `/workspace/<name>/`; database migration `031_multi_repo.sql` replaces the `repo TEXT` column with `repos JSONB`; see architecture decision #21 in `docs/design/architecture-decisions.md` for the full design
- **CLAUDE.md injection** — Claude Code runs with `--bare` which disables native CLAUDE.md discovery; skiff-init reads `CLAUDE.md` from cloned repos and prepends the content to the agent prompt, so project instructions are automatically available to agents without duplicating them in agent prompts; see architecture decision #22 in `docs/design/architecture-decisions.md` for the full design

## Dev Container Usage

When a dev container is available (`$DEV_CONTAINER_HOST` is set), use it for all build, test, and lint commands instead of running them directly. The dev container has the full project toolchain.

```bash
# Check dev container health
curl -s http://$DEV_CONTAINER_HOST/healthz

# Run tests
curl -s -X POST http://$DEV_CONTAINER_HOST/exec \
  -H "Authorization: Bearer $DEV_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd":"cd /workspace && make test","timeout":300}'

# Build
curl -s -X POST http://$DEV_CONTAINER_HOST/exec \
  -H "Authorization: Bearer $DEV_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd":"cd /workspace && go build ./...","timeout":120}'

# Run go vet
curl -s -X POST http://$DEV_CONTAINER_HOST/exec \
  -H "Authorization: Bearer $DEV_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"cmd":"cd /workspace && go vet ./...","timeout":120}'
```

Do not run build/test commands directly when a dev container is available -- always use the dev container via `POST /exec`.
