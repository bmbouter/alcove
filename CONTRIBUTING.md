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
git clone https://github.com/alcove-ai/alcove.git
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
be set in `alcove.yaml` instead of environment variables. On first run, Bridge
auto-generates this file with a random `database_encryption_key`. See
`alcove.yaml.example` for the available options.

See [CLAUDE.md](CLAUDE.md) for the full set of dev commands and architecture
details.

## Development Process

Alcove development is driven by its own SDLC pipelines. Developers focus on
alignment and coordination — the pipeline handles implementation, review, and
merging. All discussion happens in GitHub issues.

### Milestones (larger initiatives)

Milestones group related issues under a shared goal. They follow a structured
path to ensure alignment before any code is written.

**1. Problem statement.** Create a milestone issue describing the problem — what's
broken, what's missing, and why it matters. Tag relevant developers and agents.

**2. Problem agreement.** At least one other person must approve the problem
statement (explicit comment or thumbs-up reaction on the issue). Don't proceed
until the problem is agreed upon.

**3. Specification.** Once the problem is agreed, write a specification in the
issue (or link to a design doc). The spec should cover the approach, scope, and
any architectural decisions.

**4. Spec agreement.** At least one other person must approve the specification.
Add the `needs-planning` label to invoke the [Milestone Planner](/.alcove/workflows/milestone-planner.yml)
agent, which contributes a multi-perspective implementation plan directly in the
issue. Iterate via issue comments — the planner re-runs on each new comment.

**5. Create issues.** Once the spec is agreed, break the milestone into individual
issues. Each issue should be a single deliverable unit that the SDLC pipeline can
handle independently.

### Issues and bugs (individual work items)

Issues not attached to a milestone follow the same alignment pattern at a smaller
scale.

**1. Problem statement.** File an issue describing the problem or feature request.
Include what you were trying to do, what happened (or what's missing), and why it
matters.

**2. Agreement.** At least one other person must approve the problem statement.

**3. Implementation plan.** Add details about the expected approach — which files,
what the fix or feature should look like, acceptance criteria. For non-trivial
issues, add the `needs-planning` label to get an agent-generated implementation
plan.

**4. Plan agreement.** At least one other person must approve the implementation
plan.

**5. Trigger the SDLC.** The issue author adds the `ready-for-dev` label. The
[SDLC pipeline](/.alcove/workflows/feature-pipeline.yml) claims the issue,
implements the change, creates a PR, runs CI (with fix loops), runs parallel code
and security review (with revision loops), and merges.

**6. Monitor.** The person who triggered the pipeline is responsible for
monitoring it to completion. Check the workflow run status on the Alcove
dashboard or via the API. If the pipeline stalls or fails (e.g., deadlocked
steps, CI timeout, merge conflicts it can't resolve), investigate and either
fix the underlying issue or re-trigger the pipeline. Notify the team on the
GitHub issue if the pipeline did not complete as expected.

### Sign-off rules

- **Author cannot self-approve.** At least one person other than the author must
  sign off at each gate (problem, spec, plan).
- **Approval format.** Either an explicit approval comment or a thumbs-up reaction
  on the relevant comment or issue body.
- **No silent escalation.** If the scope grows during planning, re-confirm
  agreement before proceeding.

### Overlapping work

When multiple issues touch the same code, let the pipeline handle it. The SDLC
pipeline includes rebase and conflict-resolve steps. If conflicts arise, the
pipeline rebases and dispatches an agent to resolve them. File issues
independently — don't serialize work waiting for other issues to merge.

### Releases

Releases are triggered manually today. The
[release pipeline](/.alcove/workflows/release-pipeline.yml) handles changelog
generation, tagging, and deployment to staging.

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

Open an issue at https://github.com/alcove-ai/alcove/issues with:

- What you were trying to do
- What happened instead
- Steps to reproduce
- Relevant logs or error messages

## License

By contributing to Alcove, you agree that your contributions will be licensed
under the [Apache License, Version 2.0](LICENSE).
