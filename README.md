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

## Security Principles

Alcove is built around five north-star security principles that ensure safe AI agent execution:

- **Safe Sandboxing** — Every task runs in an ephemeral, network-isolated container that's never reused
- **Data Leakage Protection** — Real credentials are never available to the LLM; Gate injects them at proxy level
- **Human-in-the-Loop Controls** — Security profiles and approval gates ensure humans control what agents can access
- **Audit Records** — Every network request and LLM interaction is logged with full session transcripts
- **Least Privilege** — Gate's network proxy denies by default; only explicitly allowed operations are permitted

For implementation details, see [Security Principles](docs/design/security-principles.md).

## Components

| Component | Name | Purpose |
|-----------|------|---------|
| Controller | **Bridge** | Coordination, dashboard, REST API, scheduler |
| Worker | **Skiff** | Ephemeral Claude Code execution (k8s Job / podman run) |
| Auth Proxy | **Gate** | Network sandbox, token replacement, LLM API proxy |
| Message Bus | **Hail** | Task dispatch and status (NATS) |
| Session Store | **Ledger** | Audit trail, transcripts, proxy logs (PostgreSQL) |

## Status

Phase 1 complete with Phase 2 features underway. Working end-to-end pipeline:
CLI, REST API, dashboard, task scheduler, ephemeral container execution
(podman and Kubernetes), NATS messaging, PostgreSQL session storage, credential
management, dual auth backends, skill/agent repos (Claude Code plugins loaded
into workers), YAML task definitions (version-controlled reusable tasks from
git repos), and per-task NetworkPolicy enforcement on Kubernetes. See
[Implementation Status](docs/design/implementation-status.md) for details.

## Requirements

- **Go 1.25+**
- **podman** (rootless)
- **make**
- An LLM provider (Anthropic API key or Google Vertex AI credentials)

## CLI Installation

### One-Line Install (Linux/macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

### Windows (PowerShell)

```powershell
iex (iwr https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1).Content
```

### Manual Download

Download platform-specific binaries from [GitHub Releases](https://github.com/bmbouter/alcove/releases/latest):

- **Linux AMD64**: `alcove-linux-amd64`
- **Linux ARM64**: `alcove-linux-arm64`
- **macOS Intel**: `alcove-darwin-amd64`
- **macOS Apple Silicon**: `alcove-darwin-arm64`
- **Windows AMD64**: `alcove-windows-amd64.exe`

### Verify Installation

```bash
alcove version
alcove --help
```

### Getting Started with CLI

```bash
# Connect to your Bridge instance
alcove login https://your-bridge-instance.com

# Submit a task
alcove run "Fix the bug in the login function"

# List recent sessions
alcove list --since 24h

# Follow logs in real-time
alcove logs <session-id> --follow
```

## Quick Start

```bash
git clone https://github.com/bmbouter/alcove.git
cd alcove
make up
# make up auto-generates alcove.yaml with a random database encryption key
# Open http://localhost:8080 — log in with admin/admin
# Configure LLM credentials and providers in the dashboard
```

For the full setup walkthrough, see the [Getting Started](docs/getting-started.md) guide.

## Documentation

| Document | Description |
|----------|-------------|
| [Getting Started](docs/getting-started.md) | Setup, configuration, and first task walkthrough |
| [Configuration](docs/configuration.md) | All environment variables, skill repos, task definitions |
| [API Reference](docs/api-reference.md) | REST API endpoints, request/response formats |
| [CLI Reference](docs/cli-reference.md) | All commands, flags, and usage examples |
| [Development Guide](docs/development-guide.md) | Building, testing, adding features |
| [Architecture](docs/design/architecture.md) | Component design, deployment diagrams, roadmap |
| [Security Principles](docs/design/security-principles.md) | Five north-star security principles with implementation details |
| [Architecture Decisions](docs/design/architecture-decisions.md) | Resolved design choices (18 decisions) |
| [Implementation Status](docs/design/implementation-status.md) | Current state, what works, what is next |
| [Problem Statement](docs/design/problem-statement.md) | Why ephemeral agents |
| [Credential Management](docs/design/credential-management.md) | How Bridge manages credentials and passes tokens to Gate |
| [Auth Backends](docs/design/auth-backends.md) | Authentication and authorization design |
| [Changelog](docs/changelog.md) | Release history and version notes |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, build instructions,
and how to submit changes.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
