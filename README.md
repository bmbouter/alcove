<p align="center">
  <img src="web/logo.svg" alt="Alcove" width="120">
</p>

<h1 align="center">Alcove</h1>

<p align="center">Sandboxed AI coding agents on OpenShift/Kubernetes — natively.</p>

Alcove runs AI coding agents (Claude Code) in ephemeral, network-isolated
containers with full auditability. Each task gets a fresh container, a
scoped authorization proxy, and a complete session transcript. No persistent
state crosses task boundaries.

## Why Ephemeral?

Long-running AI agents accumulate context window contamination, credential
exposure, and filesystem state that creates security and reliability problems.
Alcove takes the opposite approach: one task, one container, then destroy it.
See [Problem Statement](docs/design/problem-statement.md).

## Components

| Component | Name | Purpose |
|-----------|------|---------|
| Controller | **Bridge** | Coordination, dashboard, REST API, scheduler |
| Worker | **Skiff** | Ephemeral Claude Code execution (k8s Job / podman run) |
| Auth Proxy | **Gate** | Network sandbox, token replacement, LLM API proxy |
| Message Bus | **Hail** | Task dispatch and status (NATS) |
| Session Store | **Ledger** | Audit trail, transcripts, proxy logs (PostgreSQL) |

## Status

Phase 1 implemented. The project has a working end-to-end pipeline: CLI, REST
API, dashboard, task scheduler, ephemeral container execution (podman), NATS
messaging, PostgreSQL session storage, credential management, and dual auth
backends (Argon2id passwords + session tokens). See
[Implementation Status](docs/design/implementation-status.md) for details on
what is built and what is planned next.

## Requirements

- **Go 1.25+**
- **podman** (rootless)
- **make**
- An LLM provider (Anthropic API key or Google Vertex AI credentials)

## Quick Start

```bash
git clone https://github.com/bmbouter/alcove.git
cd alcove
make up
# make up auto-generates alcove.conf with a random credential key
# Open http://localhost:8080 — log in with admin/admin
# Configure LLM credentials and providers in the dashboard
```

For the full setup walkthrough, see the [Getting Started](docs/getting-started.md) guide.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Setup, configuration, and first task walkthrough |
| [CLI Reference](docs/cli-reference.md) | All commands, flags, and usage examples |
| [Architecture](docs/design/architecture.md) | Component design, deployment diagrams, roadmap |
| [Architecture Decisions](docs/design/architecture-decisions.md) | Resolved design choices (18 decisions) |
| [Implementation Status](docs/design/implementation-status.md) | Current state, what works, what is next |
| [Problem Statement](docs/design/problem-statement.md) | Why ephemeral agents |
| [Credential Management](docs/design/credential-management.md) | How Bridge manages credentials and passes tokens to Gate |
| [Auth Backends](docs/design/auth-backends.md) | Authentication and authorization design |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, build instructions,
and how to submit changes.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
