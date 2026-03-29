# Alcove — Sandboxed AI Coding Agents on OpenShift/Kubernetes

## What This Is

Alcove runs AI coding agents (Claude Code) in ephemeral, network-isolated
containers. Each task gets a fresh container, a scoped authorization proxy, and
a complete session transcript. No persistent state crosses task boundaries.

**Language:** Go 1.25 | **License:** Apache-2.0

## Components

| Name | Role | Binary | k8s Resource |
|------|------|--------|-------------|
| **Bridge** | Controller, REST API, dashboard, scheduler | `cmd/bridge` | Deployment |
| **Skiff** | Ephemeral Claude Code worker | `cmd/skiff-init` | Job / `podman run --rm` |
| **Gate** | Auth proxy sidecar (token swap, LLM proxy, SCM proxy, scope enforcement) | `cmd/gate` | Sidecar in Skiff pod |
| **Hail** | Message bus (NATS) | external | Deployment |
| **Ledger** | Session store (PostgreSQL) | external | Deployment + PVC |

## Architecture

```
Bridge → Hail (NATS) → Skiff Pod [skiff container + gate sidecar] → Gate → External Services
                                                                  → Ledger (PostgreSQL)
```

- Skiff pods are ephemeral: one task, one container, then destroyed
- Gate is a sidecar (shares network namespace with Skiff)
- Gate proxies ALL external traffic including LLM API calls (Skiff has no real credentials)
- NetworkPolicy enforces this on OpenShift; dual-network isolation (`--internal` flag) on podman

## Design Documents

Read these for full context:

1. `docs/design/implementation-status.md` — **START HERE** — current state, what works, what's next
2. `docs/design/architecture.md` — component design, deployment diagrams, network isolation, roadmap
3. `docs/design/architecture-decisions.md` — 18 resolved decisions, CLI design, config format, repo layout
4. `docs/design/problem-statement.md` — why ephemeral agents
5. `docs/design/credential-management.md` — credential storage, encryption, OAuth2 token flow
6. `docs/design/auth-backends.md` — dual auth backend design (memory vs postgres)
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

# Run Bridge locally (after make dev-infra)
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
ALCOVE_NETWORK="alcove-internal" \
ALCOVE_EXTERNAL_NETWORK="alcove-external" \
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

- **Gate is a sidecar** per Skiff pod (not shared service) — credential isolation
- **No MITM TLS** — protocol-level interception (HTTP_PROXY, git credential helpers)
- **LLM keys never enter Skiff** — Gate proxies LLM API calls, injects keys; Gate also translates Anthropic API format to Vertex AI format when using Vertex AI (`GATE_VERTEX_PROJECT`, `GATE_VERTEX_REGION`)
- **Fresh git clone per task** — `git clone --depth=1`, no persistent volumes
- **NATS for messaging** — status updates and cancellation only (task config via env vars)
- **PostgreSQL only** for Ledger (no S3 in Phase 1)
- **Podman + k8s** dual runtime via `Runtime` interface in `internal/runtime/`
- **Credential management via Bridge** — Bridge pre-fetches OAuth2 tokens, Gate receives only short-lived tokens
- **Dual auth backends** — `AUTH_BACKEND=memory` (default) or `postgres`, explicit selection
- **SCM API proxied through Gate** — `/github/` and `/gitlab/` endpoints with dummy tokens, operation-level scope enforcement, real credentials never enter Skiff
- **Custom migration runner** — embedded SQL files, advisory locking, no external dependencies
- **`alcove.conf` for infrastructure settings** — config file search order: `ALCOVE_CONFIG_FILE` env var → `./alcove.conf` → `/etc/alcove/alcove.conf`; env vars always override; `credential_key` is required (Bridge refuses to start without it); `make up` auto-generates the file for local dev; file is gitignored
