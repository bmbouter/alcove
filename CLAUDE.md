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
| **Hail** | Message bus (NATS) | external | Deployment |
| **Ledger** | Session store (PostgreSQL) | external | Deployment + PVC |

## Architecture

```
Bridge → Hail (NATS) → Skiff Pod [skiff container + gate sidecar] → Gate → External Services
                                                                  → Ledger (PostgreSQL)
```

- Skiff pods are ephemeral: one session, one container, then destroyed
- Gate is a sidecar (shares network namespace with Skiff)
- Gate proxies ALL external traffic including LLM API calls (Skiff has no real credentials)
- NetworkPolicy enforces this on OpenShift; dual-network isolation (`--internal` flag) on podman; no network isolation on Docker (see Key Decisions)

## Design Documents

Read these for full context:

1. `docs/design/implementation-status.md` — **START HERE** — current state, what works, what's next
2. `docs/design/architecture.md` — component design, deployment diagrams, network isolation, roadmap
3. `docs/design/architecture-decisions.md` — 18 resolved decisions, CLI design, config format, repo layout
4. `docs/design/problem-statement.md` — why ephemeral agents
5. `docs/design/credential-management.md` — credential storage, encryption, OAuth2 token flow
6. `docs/design/auth-backends.md` — auth backend design (memory, postgres, rh-identity)
7. `docs/design/gate-scm-authorization.md` — SCM proxy endpoints, operation taxonomy, security model

## Quick Commands

```bash
# Build
make build                    # Build all Go binaries to bin/
make build-images             # Build container images with podman
make test                     # Run tests

# Full environment (build + start everything)
make up                       # Build images and start Bridge + NATS + PostgreSQL
make down                     # Stop everything
make logs                     # Show logs from all containers

# Dev environment (infrastructure only, run Bridge locally)
make dev-infra                # Start only NATS + PostgreSQL on podman
make dev-up                   # Start full environment (Bridge + NATS + PostgreSQL)
make dev-down                 # Stop everything
make dev-reset                # Stop + remove volumes

# Run Bridge locally with Podman (after make dev-infra)
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
ALCOVE_NETWORK="alcove-internal" \
ALCOVE_EXTERNAL_NETWORK="alcove-external" \
./bin/bridge

# Run Bridge locally with Docker (after infrastructure setup)
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=docker \
./bin/bridge

# Upgrade Bridge (running sessions continue undisturbed)
make build-images
podman run -d --replace --name alcove-bridge ...  # same args as dev-up
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
- **Per-item catalog granularity** — catalog has a two-level hierarchy: sources (git repos, unit of distribution) and items (plugins, agents, LSPs, MCPs); teams toggle individual items, not whole sources; enabled agents are referenced in workflow steps as `source/item` slugs; workflow definitions are validated at sync time for unknown/disabled agent references
- **Workflow graph with bounded cycles** — workflows support agent steps (Skiff pods) and bridge steps (deterministic `create-pr`/`await-ci`/`merge-pr` actions); `depends` expressions with `&&`/`||`; `max_iterations` prevents infinite loops in review/revision cycles
- **YAML is the single source of truth for schedules, security profiles, and tools** — no API-based creation, update, or deletion; schedules are defined via `schedule:` in `.alcove/tasks/*.yml`; security profiles in `.alcove/security-profiles/*.yml`; tools come from catalog or builtin definitions; the API provides read-only access to synced data
- **`alcove.yaml` for infrastructure settings** — config file search order: `ALCOVE_CONFIG_FILE` env var → `./alcove.yaml` → `/etc/alcove/alcove.yaml`; env vars always override; `database_encryption_key` is required (Bridge refuses to start without it); `make up` auto-generates the file for local dev; file is gitignored
