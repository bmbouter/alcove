# Development Guide

This guide covers the day-to-day workflow for contributing to Alcove: building,
testing, adding features, and understanding the codebase conventions.

## Repository Layout

```
alcove/
  cmd/
    alcove/          CLI client
    bridge/          Bridge controller (REST API, scheduler, dashboard)
    gate/            Gate auth proxy sidecar
    skiff-init/      Skiff ephemeral worker init process
  internal/
    auth/            Authentication backends (Authenticator interface)
    bridge/          Bridge internals
      api.go         REST API handlers and route registration
      config.go      Bridge configuration (env var parsing)
      credentials.go Credential storage and encryption
      dispatcher.go  Task dispatching to runtime
      scheduler.go   Background task scheduling
      migrations/    Embedded SQL migration files
    gate/            Gate proxy, scope enforcement, domain allowlist
    hail/            NATS messaging helpers
    ledger/          PostgreSQL session store helpers
    runtime/         Runtime interface and implementations (podman, kubernetes)
    types.go         Shared types (Session, Scope, TranscriptEvent, etc.)
  build/
    alcove-credential-helper  Git credential helper binary (installed in Skiff image)
    Containerfile.*  Multi-stage container image definitions
  deploy/            Kubernetes/OpenShift manifests
  docs/              Documentation
  web/               Dashboard static files
  bin/               Build output (gitignored)
```

## Build and Test

All build and test operations use `make`. Run `make help` to see all targets.

### Building binaries

```bash
make build
```

This compiles all four binaries (`bridge`, `gate`, `skiff-init`, `alcove`) into
the `bin/` directory. Version information is injected via `-ldflags` from
`git describe`.

### Building container images

```bash
make build-images
```

Builds three container images with podman:

- `localhost/alcove-bridge:<version>`
- `localhost/alcove-gate:<version>`
- `localhost/alcove-skiff-base:<version>`

### Running tests

```bash
make test
```

Runs `go test ./...` across the entire module.

### Testing network isolation

```bash
make test-network
```

Validates the dual-network setup by checking that the internal and external
podman networks are configured correctly.

### Linting

```bash
make lint
```

Runs `go vet` and `staticcheck`. Install staticcheck first:

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
```

## Local Development

There are two ways to run Alcove locally. Both require podman.

### Mode 1: Fully containerized

Build images and start everything in containers:

```bash
make up        # build-images + dev-up
make logs      # tail logs from all containers
make down      # stop everything
make dev-reset # stop + remove volumes
```

This starts PostgreSQL (Ledger), NATS (Hail), and Bridge as containers on a
dual-network pattern: `alcove-internal` (an `--internal` network with no
external access) and `alcove-external` (for Gate egress). Skiff containers are
attached only to the internal network; Gate bridges both networks. The Bridge
container gets access to the host's podman socket so it can create Skiff+Gate
containers.

The dashboard is available at `http://localhost:8080`. Log in with `admin` /
`admin` and change the password after first login.

### Mode 2: Infrastructure in containers, Bridge locally

Start only NATS and PostgreSQL in containers, then run Bridge as a local
process. This is faster for iterating on Bridge code since you skip the image
build step.

```bash
make dev-infra    # start PostgreSQL + NATS only

# In another terminal:
make build
LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
HAIL_URL="nats://localhost:4222" \
RUNTIME=podman \
./bin/bridge
```

### Environment variables

Bridge reads these environment variables:

| Variable | Purpose | Example |
|----------|---------|---------|
| `LEDGER_DATABASE_URL` | PostgreSQL connection string | `postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable` |
| `HAIL_URL` | NATS server URL | `nats://localhost:4222` |
| `RUNTIME` | Container runtime to use | `podman` or `kubernetes` |
| `SKIFF_IMAGE` | Skiff container image | `localhost/alcove-skiff-base:dev` |
| `GATE_IMAGE` | Gate container image | `localhost/alcove-gate:dev` |
| `ALCOVE_WEB_DIR` | Path to dashboard static files | `/web` or `./web` |
| `ALCOVE_NETWORK` | Podman internal network name | `alcove-internal` |
| `ALCOVE_EXTERNAL_NETWORK` | Podman external network for Gate egress | `alcove-external` |
| `AUTH_BACKEND` | Authentication backend | `memory` or `postgres` |
| `ALCOVE_DATABASE_ENCRYPTION_KEY` | Encryption key for stored credentials | (secret string) |
| `ALCOVE_DEBUG` | Keep worker containers after exit for debugging | `true` or `false` |
| `BRIDGE_URL` | URL where Bridge is reachable by Skiff/Gate | `http://host.containers.internal:8080` |
| `SKIFF_HAIL_URL` | NATS URL as seen from inside Skiff containers | `nats://host.containers.internal:4222` |

Set these as environment variables before running Bridge.

## Adding a Database Migration

Migrations live in `internal/bridge/migrations/` as embedded SQL files. They
are applied automatically on Bridge startup.

### Step-by-step

1. Determine the next version number by looking at existing files:

   ```bash
   ls internal/bridge/migrations/
   # 001_initial_schema.sql
   ```

2. Create a new file with the next numeric prefix and a descriptive name:

   ```bash
   touch internal/bridge/migrations/002_add_task_labels.sql
   ```

3. Write the SQL. Use `IF NOT EXISTS` for safety. Example:

   ```sql
   -- 002_add_task_labels.sql
   -- Adds a labels column to the sessions table for task categorization.

   ALTER TABLE sessions ADD COLUMN IF NOT EXISTS labels JSONB DEFAULT '{}';
   CREATE INDEX IF NOT EXISTS idx_sessions_labels ON sessions USING GIN (labels);
   ```

4. That is it. The migration runner embeds the `migrations/` directory at
   compile time via `//go:embed`. When Bridge starts, it:
   - Acquires a PostgreSQL advisory lock to prevent concurrent migration runs
   - Reads the `schema_migrations` table to find which versions are applied
   - Sorts migration files by numeric prefix
   - Runs each pending migration in its own transaction
   - Records the version in `schema_migrations`

### Naming convention

```
NNN_short_description.sql
```

- `NNN` is a zero-padded integer (001, 002, 003, ...)
- The description uses underscores, lowercase
- The numeric prefix is parsed by splitting on the first `_` and converting to
  an integer, so `001` and `1` both resolve to version 1

### Rules

- Each migration runs in a single transaction. If it fails, the transaction
  rolls back and Bridge will not start.
- Migrations are idempotent by convention (use `IF NOT EXISTS`, `ADD COLUMN IF
  NOT EXISTS`, etc.).
- Never modify an already-applied migration. Always create a new one.

## Adding an API Endpoint

The Bridge REST API is implemented in `internal/bridge/api.go` using the
standard library `net/http` package. There are no frameworks.

### Handler pattern

Every handler follows this structure:

```go
func (a *API) handleMyResource(w http.ResponseWriter, r *http.Request) {
    // 1. Check HTTP method.
    if r.Method != http.MethodGet {
        respondError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    // 2. Parse and validate input (path params, query params, or JSON body).
    var req MyRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        respondError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
        return
    }

    // 3. Perform the operation (database query, dispatch, etc.).
    result, err := a.doSomething(r.Context(), req)
    if err != nil {
        log.Printf("error: doing something: %v", err)
        respondError(w, http.StatusInternalServerError, "failed to do something")
        return
    }

    // 4. Respond with JSON.
    respondJSON(w, http.StatusOK, result)
}
```

For resources that support multiple HTTP methods on the same path, use a
`switch` on `r.Method`:

```go
func (a *API) handleMyResource(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        // list or get
    case http.MethodPost:
        // create
    default:
        respondError(w, http.StatusMethodNotAllowed, "method not allowed")
    }
}
```

### Route registration

Register the handler in the `RegisterRoutes` method:

```go
func (a *API) RegisterRoutes(mux *http.ServeMux) {
    // ... existing routes ...
    mux.HandleFunc("/api/v1/myresource", a.handleMyResource)
    mux.HandleFunc("/api/v1/myresource/", a.handleMyResourceByID)
}
```

Routes follow the pattern `/api/v1/<resource>` for collection endpoints and
`/api/v1/<resource>/` (trailing slash) for individual resource endpoints. The
trailing-slash handler parses the ID from the URL path manually:

```go
func (a *API) handleMyResourceByID(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/api/v1/myresource/")
    if id == "" {
        respondError(w, http.StatusBadRequest, "id required")
        return
    }
    // ...
}
```

### Response helpers

Use the two provided helpers for all responses:

```go
respondJSON(w, http.StatusOK, data)           // success with JSON body
respondError(w, http.StatusBadRequest, "msg") // error with {"error": "msg"}
```

### Authentication

API routes are protected by the auth middleware. The authenticated username is
available via `r.Header.Get("X-Alcove-User")`. These paths are public:

- `/api/v1/auth/login`
- `/api/v1/health`
- `/api/v1/internal/*` (internal service-to-service calls)

Additionally, POST requests to paths ending in `/transcript`, `/status`, or
`/proxy-log` are exempt from user authentication. These are session ingestion
paths used by Skiff and Gate to report data back to Bridge. They are
authenticated via session tokens instead of user tokens. See
`isSessionIngestionPath()` in `internal/auth/auth.go`.

## Adding a New Auth Backend

The authentication system is defined by the `Authenticator` interface in
`internal/auth/auth.go`:

```go
type Authenticator interface {
    Authenticate(username, password string) (string, error)
    ValidateToken(token string) (string, bool)
    InvalidateToken(token string)
}
```

To add a new backend (for example, LDAP or OIDC):

1. Create a new file in `internal/auth/`, e.g., `ldap.go`.

2. Define a struct that implements the `Authenticator` interface:

   ```go
   type LDAPAuthenticator struct {
       serverURL string
       baseDN    string
       // ...
   }

   func (l *LDAPAuthenticator) Authenticate(username, password string) (string, error) {
       // Bind to LDAP, verify credentials.
       // On success, generate and store a session token.
       token, err := generateToken()
       if err != nil {
           return "", err
       }
       // Store token -> username mapping with expiry.
       return token, nil
   }

   func (l *LDAPAuthenticator) ValidateToken(token string) (string, bool) {
       // Look up token, check expiry, return username.
       return username, true
   }

   func (l *LDAPAuthenticator) InvalidateToken(token string) {
       // Remove the token from the store.
   }
   ```

3. Wire it into Bridge startup based on configuration (e.g., an
   `AUTH_BACKEND` environment variable).

If the backend also supports user management, implement the `UserManager`
interface:

```go
type UserManager interface {
    CreateUser(ctx context.Context, username, password string) error
    DeleteUser(ctx context.Context, username string) error
    ListUsers(ctx context.Context) ([]UserInfo, error)
    ChangePassword(ctx context.Context, username, newPassword string) error
}
```

### Password hashing

Use the provided `HashPassword` and `VerifyPassword` functions from the `auth`
package. They use argon2id with these parameters: 64 MB memory, 3 iterations,
parallelism 4, 32-byte key.

## Runtime Backends

The `Runtime` interface in `internal/runtime/runtime.go` abstracts over
container runtimes. There are two implementations:

- **PodmanRuntime** (`podman.go`) -- creates Skiff and Gate as separate
  containers on dual podman networks (`--internal` for isolation)
- **KubernetesRuntime** (`kubernetes.go`) -- creates a k8s Job with Gate as a
  native sidecar (init container with `restartPolicy: Always`) and Skiff as the
  main container. Also creates a per-task NetworkPolicy restricting egress.

Set `RUNTIME=podman` or `RUNTIME=kubernetes` to select the backend.

### Kubernetes Runtime Details

The Kubernetes runtime uses direct client-go API calls (no operator or CRDs).
Key design points:

- **Jobs with native sidecars:** Gate runs as an init container with
  `restartPolicy: Always`, which makes it a native sidecar that starts before
  and stops after the main Skiff container. Gate and Skiff share the pod's
  network namespace, so proxy env vars point to `localhost:8443`.
- **NetworkPolicy per task:** each Job gets a NetworkPolicy restricting egress
  to only the Gate sidecar (on localhost), Hail (NATS), and Bridge.
- **OpenShift compatible:** security contexts use `restricted-v2` SCC
  (non-root, drop all capabilities, `seccompProfile: RuntimeDefault`).
- **Minimal RBAC:** Bridge needs create/get/list/delete on Jobs and
  NetworkPolicies in its namespace.
- **Namespace detection:** uses `ALCOVE_NAMESPACE` env var, then in-cluster
  service account namespace, then defaults to `alcove`.

To test with Kubernetes locally, use `kind` or `minikube`:

```bash
RUNTIME=kubernetes KUBECONFIG=~/.kube/config ./bin/bridge
```

### Adding a New Runtime Backend

```go
type Runtime interface {
    RunTask(ctx context.Context, spec TaskSpec) (TaskHandle, error)
    CancelTask(ctx context.Context, handle TaskHandle) error
    TaskStatus(ctx context.Context, handle TaskHandle) (string, error)
    EnsureService(ctx context.Context, spec ServiceSpec) error
    StopService(ctx context.Context, name string) error
    CreateVolume(ctx context.Context, name string) (string, error)
    Info(ctx context.Context) (RuntimeInfo, error)
}
```

To add a new runtime:

1. Create a new file in `internal/runtime/`.
2. Implement all seven methods. `RunTask` must start both Skiff and Gate with
   shared networking and proxy configuration.
3. Wire it into Bridge startup based on the `RUNTIME` environment variable.

## Skill / Agent Repos and Task Definitions

### Skill / Agent Repos

Skill repos are git repositories containing Claude Code plugins or lola
modules. Skiff auto-detects the format based on the repo structure:

**Claude Code plugin** (detected by `.claude-plugin/plugin.json`):

```
my-skills-repo/
  .claude-plugin/
    plugin.json       # Plugin manifest
  skills/             # Skill definitions
  agents/             # Agent definitions
```

**Lola module** (detected by `module/` directory):

```
my-lola-repo/
  module/             # Lola module definitions
```

Users do not need to specify the format. Skiff checks for each structure
and loads the repo accordingly.

At dispatch time, Bridge reads system-wide and per-user skill repos from the
settings store, merges them, and passes the JSON to Skiff as
`ALCOVE_SKILL_REPOS`. Skiff clones each repo and passes the directories to
`claude` via `--plugin-dir` flags.

The relevant code paths:

- `internal/bridge/settings.go` -- `SkillRepo` type, `GetSystemSkillRepos()`,
  `SetSystemSkillRepos()`, `GetUserSkillRepos()`, `SetUserSkillRepos()`
- `internal/bridge/dispatcher.go` -- merges skill repos and sets
  `ALCOVE_SKILL_REPOS` env var
- `cmd/skiff-init/main.go` -- `loadSkillRepos()` clones repos and builds
  `--plugin-dir` flags

### Task Definition YAML Format

Task definitions are YAML files in `.alcove/tasks/*.yml` within a task repo:

```yaml
name: run-tests
prompt: |
  Run the full test suite and fix any failures.
repo: https://github.com/org/myproject.git
provider: anthropic
model: claude-sonnet-4-20250514
timeout: 1800
budget_usd: 5.0
profiles:
  - read-only-github
tools:
  - github
schedule: "0 2 * * *"
```

All fields except `name` and `prompt` are optional. The `schedule` field uses
standard 5-field cron syntax. When a schedule is present, Bridge creates a
corresponding schedule entry automatically.

### Testing with Task Repos

To test task repo syncing locally:

1. Create a test git repo with a `.alcove/tasks/` directory containing YAML
   task files.
2. Push it to a Git host or use a local bare repo.
3. Register the repo via the API or dashboard.
4. Wait for the sync interval (default 5 minutes) or trigger a manual sync
   via `POST /api/v1/task-definitions/sync`.
5. Check the dashboard or `GET /api/v1/task-definitions` to verify the tasks
   appear.

## Gate SCM Proxy Endpoints

Gate exposes `/github/` and `/gitlab/` reverse-proxy endpoints that forward
requests to the upstream GitHub and GitLab APIs. Inside Skiff, the `gh` and
`glab` CLIs are configured via `GITHUB_API_URL` and `GITLAB_API_URL` to point
at these local Gate endpoints. Gate inspects each request, enforces
operation-level scope (e.g., allowing `create_pr_draft` but blocking
`merge_pr`), injects real SCM credentials, and forwards to the upstream API.
See `internal/gate/` for the proxy implementation and
`docs/design/gate-scm-authorization.md` for the full design.

## Testing Patterns

### TestHelperProcess for CLI wrappers

The `PodmanRuntime` tests in `internal/runtime/podman_test.go` demonstrate how
to test code that shells out to external commands (like `podman`) without
requiring the actual binary. This technique is from
[https://npf.io/2015/06/testing-exec-command/](https://npf.io/2015/06/testing-exec-command/).

The pattern has three parts:

**1. A fake exec function factory:**

```go
func fakeExecCommand(t *testing.T, stdout string, exitCode int) (
    func(ctx context.Context, name string, args ...string) *exec.Cmd,
    *[][]string,
) {
    var calls [][]string
    fn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
        calls = append(calls, append([]string{name}, args...))
        cs := []string{"-test.run=TestHelperProcess", "--", name}
        cs = append(cs, args...)
        cmd := exec.CommandContext(ctx, os.Args[0], cs...)
        cmd.Env = []string{
            "GO_WANT_HELPER_PROCESS=1",
            fmt.Sprintf("GO_HELPER_STDOUT=%s", stdout),
            fmt.Sprintf("GO_HELPER_EXIT_CODE=%d", exitCode),
        }
        return cmd
    }
    return fn, &calls
}
```

**2. The helper process test (not a real test):**

```go
func TestHelperProcess(t *testing.T) {
    if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
        return
    }
    fmt.Fprint(os.Stdout, os.Getenv("GO_HELPER_STDOUT"))
    exitCode := 0
    if code := os.Getenv("GO_HELPER_EXIT_CODE"); code != "" && code != "0" {
        exitCode = 1
    }
    os.Exit(exitCode)
}
```

**3. Usage in tests:**

```go
func TestRunTask_CommandConstruction(t *testing.T) {
    execFn, calls := fakeExecCommand(t, "container-id-123\n", 0)
    p := &PodmanRuntime{
        PodmanBin:   "podman",
        execCommand: execFn,
    }

    spec := TaskSpec{TaskID: "task-1", Image: "skiff:latest", GateImage: "gate:latest"}
    handle, err := p.RunTask(context.Background(), spec)
    // ... assertions on handle and *calls ...
}
```

For commands that need different responses on successive calls, use
`fakeExecCommandMulti` with a slice of `fakeResponse` structs:

```go
responses := []fakeResponse{
    {stdout: "[]", exitCode: 0},   // first call returns empty
    {stdout: "cid\n", exitCode: 0}, // second call succeeds
}
execFn, calls := fakeExecCommandMulti(t, responses)
```

This lets you verify the exact command-line arguments passed to `podman`
without running real containers.

## Container Images

### Multi-stage builds

All Containerfiles use multi-stage builds:

1. **Build stage:** `golang:1.25` -- compiles the Go binary with
   `CGO_ENABLED=0` for a static binary
2. **Runtime stage:** varies per component (see table below)

Example from `build/Containerfile.bridge`:

```dockerfile
FROM docker.io/library/golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/bridge ./cmd/bridge

FROM registry.access.redhat.com/ubi9/ubi:latest
COPY --from=builder /out/bridge /usr/local/bin/bridge
RUN dnf install -y podman && \
    useradd -r -u 1001 -s /sbin/nologin alcove && \
    dnf clean all
USER 1001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/bridge"]
```

### Naming convention

Local development images use this tag format:

```
localhost/alcove-<component>:<version>
```

Examples:
- `localhost/alcove-bridge:dev`
- `localhost/alcove-gate:dev`
- `localhost/alcove-skiff-base:dev`

The version defaults to the output of `git describe --tags --always --dirty`,
or `dev` if not in a git repository.

### Publishing to GitHub Container Registry

Pre-built images are available at `ghcr.io/bmbouter/alcove-<component>`.

**Pulling images:**

```bash
make pull                    # Pull latest version
make pull VERSION=v0.1.0     # Pull specific version
```

**Pushing images (maintainers):**

Requires a GitHub PAT with `write:packages` scope.

```bash
# Login (one-time per session)
GHCR_TOKEN=ghp_xxx GHCR_USER=youruser make login-registry

# Build and push
make push VERSION=v0.1.0
```

**Release process:**

```bash
git tag -a v0.1.0 -m "Release v0.1.0"
make push VERSION=v0.1.0
git push origin v0.1.0
```

**Image naming:**

| Image | Registry Path |
|-------|-------------|
| Bridge | `ghcr.io/bmbouter/alcove-bridge:<version>` |
| Gate | `ghcr.io/bmbouter/alcove-gate:<version>` |
| Skiff | `ghcr.io/bmbouter/alcove-skiff-base:<version>` |

Each push also updates the `latest` tag.

### Containerfile locations

All Containerfiles live in `build/`:

| File | Image | Notes |
|------|-------|-------|
| `Containerfile.bridge` | `alcove-bridge` | Base: `ubi9/ubi` (needs podman for spawning Skiff+Gate) |
| `Containerfile.gate` | `alcove-gate` | Base: `ubi9-minimal` (lightweight proxy binary) |
| `Containerfile.skiff-base` | `alcove-skiff-base` | Base: `ubi9/ubi` (Claude Code worker environment; includes `gh`, `glab`, `alcove-credential-helper`, and git config forcing HTTPS) |
