# Getting Started with Alcove

Run Alcove on your laptop in 5 minutes. Everything runs in containers via podman.

## Prerequisites

- **podman** (rootless) -- `sudo dnf install podman` (Fedora) or equivalent
- **podman socket** -- must be running for Bridge to create worker containers
- **make** -- for build and run targets
- **Go 1.25+** -- only needed for `make build` (local binaries); not required for `make up` which builds inside containers

### Enable the podman socket

Alcove's Bridge controller creates ephemeral worker containers. It needs access
to the podman socket to do this.

```bash
systemctl --user enable --now podman.socket
```

Verify it's running:

```bash
podman info > /dev/null && echo "podman is ready"
ls $XDG_RUNTIME_DIR/podman/podman.sock && echo "socket is available"
```

## Using Pre-built Images (Optional)

Instead of building images locally, you can pull pre-built images from GitHub
Container Registry:

```bash
make pull VERSION=v0.1.0
```

This pulls all three images and tags them locally. Then use `make dev-up`
(which skips the build step) instead of `make up`.

## Quick Start

### 1. Clone

```bash
git clone https://github.com/bmbouter/alcove.git
cd alcove
```

### 2. Build and run

**Option A: Build locally** (takes ~3 min first time):
```bash
make up
```

**Option B: Use pre-built images** (faster):
```bash
make up-pull VERSION=v0.1.0
```

This builds all container images and starts:
- **Bridge** (controller + dashboard) on http://localhost:8080
- **Hail** (NATS message bus) on nats://localhost:4222
- **Ledger** (PostgreSQL) on localhost:5432

`make up` auto-generates an `alcove.yaml` file from `alcove.yaml.example` with
a random `database_encryption_key` for credential encryption. This file is gitignored.

### 3. Open the dashboard

Go to http://localhost:8080 in your browser. Log in with:
- Username: `admin`
- Password: `admin`

Change the default password after first login.

> **Red Hat deployments:** If running behind Turnpike, set
> `AUTH_BACKEND=rh-identity` to authenticate via the `X-RH-Identity` header.
> No login form is needed -- users are auto-provisioned on first request.
> See `docs/configuration.md` for details.

> **Note:** The database is ephemeral. Each `make down` + `make up` cycle wipes
> all data (containers run with `--rm`).

### 4. Start your first session

From the dashboard:
1. Click "New Session"
2. Enter a prompt like "Write a hello world Python script"
3. Select your provider
4. Click Submit

Or via CLI:
```bash
# Build the CLI
make build

# Login
./bin/alcove login http://localhost:8080

# Start a session
./bin/alcove run "Write a hello world Python script"

# Watch it live
./bin/alcove run --watch "Explain what the Alcove project does" --repo https://github.com/...
```

Or via curl:
```bash
# Get auth token
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"YOUR_PASSWORD"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

# Start a session
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Say hello","provider":"anthropic","timeout":300}'

# List sessions
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/sessions
```

### 5. Stop

```bash
make down
```

## Configuration

### System LLM

The system LLM (used for AI-powered features like the security profile builder) is
configured in `alcove.yaml` or via environment variables -- not through the
dashboard. Add one of these to your `alcove.yaml`:

```yaml
# Anthropic API
llm_provider: anthropic
llm_api_key: sk-ant-...

# or Google Vertex AI
# llm_provider: google-vertex
# llm_service_account_json: '{"type":"service_account",...}'
# llm_project: your-gcp-project-id
# llm_region: us-east5
```

The dashboard shows a read-only system LLM status. If not configured, it
displays a message directing you to edit `alcove.yaml`.

### LLM Providers (Task Execution)

Alcove also needs an LLM provider credential for running tasks with Claude
Code. Set environment variables for initial setup (auto-migrated to the
credential store on first startup), then manage via the dashboard:

```bash
ANTHROPIC_API_KEY=sk-ant-...          # Anthropic API
# or
VERTEX_PROJECT=your-gcp-project       # Google Vertex AI
VERTEX_API_KEY=your-vertex-key
```

### Debug Mode

Keep worker containers after exit for log inspection:
```bash
ALCOVE_DEBUG=true
```

### All Environment Variables

See `docs/configuration.md` for the complete list.

## Teams

Every resource in Alcove (sessions, credentials, security profiles, agent
definitions, schedules, workflows, tools, agent repos) belongs to a team. A
personal team is auto-created for each user on signup -- this acts as "My
Workspace" and cannot be deleted, renamed, or have members added.

### Creating a team in the dashboard

Click the team switcher dropdown in the top nav, then click "Create Team". Give
your team a name and it's ready to use.

### Inviting members

Open the team switcher, select your team, then go to team settings. Click "Add
Member" and enter a username. All team members have equal access -- there are no
roles within a team.

### Switching teams

Use the team switcher dropdown in the dashboard nav to select which team context
you are working in. All resources you create will belong to the active team,
and you will only see resources owned by that team.

### CLI

```bash
# Create a team
./bin/alcove teams create "My Team"

# Switch active team
./bin/alcove teams use "My Team"

# Add a member
./bin/alcove teams add-member "My Team" --username alice

# List your teams
./bin/alcove teams list
```

You can also pass `--team "My Team"` to any command to override the active team
for a single invocation.

## Skill / Agent Repos and Agent Definitions

After configuring your LLM provider, you can optionally set up skill repos and
agent definitions to extend and automate your workflow.

### Skill / Agent Repos

Skill repos are git repositories containing Claude Code plugins or lola modules
that add custom skills and agents to every task. Configure them in the dashboard
under the user menu (or under admin settings for system-wide repos).

Repos are auto-detected: if a repo contains a `.claude-plugin/plugin.json` file
it is loaded as a Claude Code plugin; if it contains a `module/` directory it is
loaded as a lola module. You just add a repo URL and Skiff figures out the
format automatically. At task dispatch time, all configured repos are cloned
into the Skiff container and loaded accordingly.

Agent repos are team-scoped. Configure them in the dashboard under team
settings (or under admin settings for system-wide repos).

### Agent Definitions

Agent definitions are YAML files stored in git repositories under
`.alcove/tasks/*.yml`. They let you define reusable, version-controlled agents
that appear in the dashboard for one-click execution.

1. Create a git repo with a `.alcove/tasks/` directory
2. Add YAML agent files (see `docs/configuration.md` for the schema)
3. Register the repo in the dashboard under **Agent Repos** (user menu)

Bridge syncs agent repos every 5 minutes. Once synced, agent definitions appear
on the dashboard where you can run them or view the source YAML. Starter
templates are available to help you get started.

## Workflow Graph

Alcove supports multi-step workflows where steps can be either **agent steps**
(dispatching a Skiff pod running Claude Code) or **bridge steps** (deterministic
actions performed by Bridge inline, no LLM involved). Workflows can contain
bounded cycles, enabling patterns like review/revision loops.

### Step Types

- **`type: agent`** (default) — dispatches a Skiff pod running Claude Code
- **`type: bridge`** — Bridge performs a deterministic action (e.g., create a PR,
  poll CI, merge a PR)

### Bridge Actions

| Action | Description |
|--------|-------------|
| `create-pr` | Creates a GitHub PR from a branch |
| `await-ci` | Polls CI status on a PR until completion |
| `merge-pr` | Merges a PR |

### Depends Expressions

Steps declare dependencies using boolean expressions instead of simple lists:

```yaml
depends: "implement.Succeeded"
depends: "code-review.Succeeded && security-review.Succeeded"
depends: "await-ci.Succeeded || revision.Succeeded"
```

### Bounded Cycles

Steps can reference each other in cycles (e.g., review fails, revision runs,
review runs again). The `max_iterations` field prevents infinite loops -- when
exhausted, the step status becomes `max_iterations_exceeded`.

### Minimal Example

```yaml
workflow:
  steps:
    - id: implement
      type: agent
      agent: dev

    - id: create-pr
      type: bridge
      action: create-pr
      depends: "implement.Succeeded"
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        title: "Fix #{{trigger.issue_number}}"
        base: main

    - id: await-ci
      type: bridge
      action: await-ci
      depends: "create-pr.Succeeded || ci-fix.Succeeded"
      max_iterations: 4
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    - id: ci-fix
      type: agent
      agent: dev
      depends: "await-ci.Failed"
      max_iterations: 3

    - id: code-review
      type: agent
      agent: reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3

    - id: revision
      type: agent
      agent: dev
      depends: "code-review.Failed"
      max_iterations: 3

    - id: merge
      type: bridge
      action: merge-pr
      depends: "code-review.Succeeded"
```

This workflow implements a full develop-review-merge cycle: implement the change,
create a PR, wait for CI (retrying up to 4 times with an agent fixing failures),
run code review (with up to 3 revision rounds), then merge.

See `docs/configuration.md` for the full workflow step field reference.

## Architecture Overview

```
┌─────────────────────────────────────────┐
│  Your laptop (podman)                    │
│                                          │
│  ┌──────────┐  ┌──────┐  ┌──────────┐  │
│  │ Bridge   │  │ Hail │  │ Ledger   │  │
│  │ :8080    │  │ NATS │  │ Postgres │  │
│  └────┬─────┘  └──────┘  └──────────┘  │
│       │ creates via podman socket        │
│  ┌────┴──────────────────────┐          │
│  │ Skiff + Gate (ephemeral)  │          │
│  │ Claude Code + auth proxy  │──► LLM   │
│  └───────────────────────────┘          │
└─────────────────────────────────────────┘
```

Each session gets a fresh Skiff container with a Gate sidecar on a dual-network
setup. Skiff is attached only to the internal network (`alcove-internal`,
created with `--internal` so it has no external access). Gate bridges both
the internal and external (`alcove-external`) networks, proxying all external
traffic and injecting LLM credentials. When the session finishes, both containers
are destroyed.

## GitHub/GitLab/JIRA Integration

Alcove can interact with GitHub, GitLab, and JIRA on behalf of your coding agent.
Register a credential, then include services in your task scope:

```bash
# Register a GitHub PAT
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"github","provider":"github","auth_type":"pat","credential":"ghp_your_token"}'

# Start a session with GitHub scope
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Fix the typo in README.md and open a PR","provider":"vertex","scope":{"services":{"github":{"repos":["org/repo"],"operations":["clone","push_branch","create_pr_draft"]}}}}'

# Register a JIRA Cloud credential (email:api_token)
curl -X POST http://localhost:8080/api/v1/credentials \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"jira","provider":"jira","auth_type":"basic","credential":"user@example.com:your-api-token"}'

# Start a session with JIRA scope
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Triage new issues in PROJ","scope":{"services":{"jira":{"repos":["PROJ"],"operations":["read_issues","search_issues","add_comment"]}}}}'
```

See [SCM Authorization Design](docs/design/gate-scm-authorization.md) for the
full operation taxonomy and security model.

## Useful Commands

| Command | Description |
|---------|-------------|
| `make up` | Build images and start everything |
| `make down` | Stop all containers |
| `make logs` | Show logs from all containers |
| `make build` | Build Go binaries locally |
| `make test` | Run tests |
| `make test-network` | Test dual-network isolation setup |

## Troubleshooting

### "podman socket not found"

Make sure the socket is enabled:
```bash
systemctl --user enable --now podman.socket
echo $XDG_RUNTIME_DIR  # Should be /run/user/$(id -u)
```

### Bridge can't create containers

Check that the podman socket is accessible from inside the Bridge container:
```bash
podman exec alcove-bridge podman --remote info
```

### Port already in use

If `make up` fails because a port is occupied, check for conflicting services:
```bash
ss -tlnp | grep -E "5432|4222|8080"
```

Stop whatever is using those ports (PostgreSQL on 5432, NATS on 4222, or
another web server on 8080) before running `make up` again.

### Tasks fail immediately

Check if the Skiff/Gate images are built:
```bash
podman images | grep alcove
```

If missing, run `make build-images`.

## Development Workflow

For Go development on Alcove itself, you can run Bridge locally
instead of in a container for faster iteration:

```bash
# Start only infrastructure
make dev-infra

# Build and run Bridge locally
make build
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
SKIFF_IMAGE="localhost/alcove-skiff:$(cat VERSION)" \
GATE_IMAGE="localhost/alcove-gate:$(cat VERSION)" \
ALCOVE_NETWORK="alcove-internal" \
ALCOVE_EXTERNAL_NETWORK="alcove-external" \
./bin/bridge
```

Bridge also needs `SKIFF_IMAGE`, `GATE_IMAGE`, `ALCOVE_NETWORK`, and
`ALCOVE_EXTERNAL_NETWORK` to create worker containers. If you built images via `make build-images`, they are tagged
with the version from the `VERSION` file (not `:dev`). You can set all of these
as environment variables before running Bridge.

This skips the Bridge container image rebuild cycle. Infrastructure
(NATS + PostgreSQL) still runs in containers.
