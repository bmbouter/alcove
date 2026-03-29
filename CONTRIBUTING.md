# Contributing to Alcove

Thanks for your interest in contributing to Alcove. This guide covers the
essentials for getting started.

## Development Setup

### Prerequisites

- Go 1.25+
- podman (rootless) with the user socket enabled
- make

Enable the podman socket if you haven't already:

```bash
systemctl --user enable --now podman.socket
```

### Clone and Build

```bash
git clone https://github.com/bmbouter/alcove.git
cd alcove
make build
```

### Run Tests

```bash
make test
```

### Run Locally

Start the infrastructure (NATS + PostgreSQL) in containers, then run Bridge
on your host for fast iteration:

```bash
make dev-infra
make build

LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
ALCOVE_NETWORK="alcove-internal" \
ALCOVE_EXTERNAL_NETWORK="alcove-external" \
./bin/bridge
```

Infrastructure settings like `database_url`, `nats_url`, and `runtime` can also
be set in `alcove.conf` instead of environment variables. On first run, Bridge
auto-generates this file with a random `credential_key`. See
`alcove.conf.example` for the available options.

See [CLAUDE.md](CLAUDE.md) for the full set of dev commands and architecture
details.

## Code Style

- Standard Go conventions. Run `go vet ./...` before submitting.
- `net/http` for HTTP servers -- no external web frameworks.
- `github.com/spf13/cobra` for CLI commands.
- Keep commits focused: one logical change per commit.

## Submitting Changes

1. Fork the repository and create a feature branch from `main`.
2. Make your changes and add tests where appropriate.
3. Run `make test` and `go vet ./...` to verify.
4. Push your branch and open a pull request against `main`.
5. Describe what your PR does and why.

## Reporting Bugs

Open an issue at https://github.com/bmbouter/alcove/issues with:

- What you were trying to do
- What happened instead
- Steps to reproduce
- Relevant logs or error messages

## License

By contributing to Alcove, you agree that your contributions will be licensed
under the [Apache License, Version 2.0](LICENSE).
