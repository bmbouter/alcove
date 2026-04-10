# Alcove CLI Reference

The `alcove` CLI dispatches and manages AI coding tasks via the Bridge API.

```
alcove [command] [flags]
```

## Installation

See the [CLI Installation Guide](cli-installation.md) for detailed installation instructions across all platforms.

### Quick Install

**Linux/macOS:**
```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

**Windows:**
```powershell
iex (iwr https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1).Content
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--server <url>` | Bridge server URL (overrides env and config file) |
| `--output <format>` | Output format: `json` or `table` (default: `table`) |

## Server Resolution

The CLI resolves the Bridge server URL using the following precedence:

1. `--server` flag (highest priority)
2. `ALCOVE_SERVER` environment variable
3. Config file at `~/.config/alcove/config.yaml` (set by `alcove login`)

If none are configured, commands that contact the Bridge will fail with an error.

---

## alcove run

Submit a coding task to the Bridge for execution in an ephemeral Skiff container.

```
alcove run [prompt] [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--repo` | string | Target repository (e.g., `org/repo`) |
| `--provider` | string | LLM provider name |
| `--model` | string | Model override (e.g., `claude-sonnet-4-20250514`) |
| `--budget` | float | Budget limit in USD (e.g., `5.00`) |
| `--timeout` | duration | Task timeout (e.g., `30m`, `1h`) |
| `--watch` | bool | Stream the session transcript via SSE after dispatch |
| `--debug` | bool | Keep containers after exit for log inspection |

### Description

Dispatches a task to the Bridge, which creates a session and launches a Skiff
container. By default, the command prints the session ID and exits immediately.
With `--watch`, it streams the live transcript until the session completes.

### Examples

```bash
# Submit a task and get the session ID
alcove run "Fix the failing test in pkg/auth"

# Submit and stream the transcript live
alcove run --watch --repo myorg/myapp "Add input validation to the login handler"

# Set a timeout and use a specific provider
alcove run --timeout 45m --provider anthropic "Refactor the database layer"

# Override the model
alcove run --model claude-sonnet-4-20250514 "Write unit tests for the auth module"

# Set a budget limit of $2 USD
alcove run --budget 2.00 --repo myorg/myapp "Add error handling to the API endpoints"

# Keep containers for debugging
alcove run --debug "Investigate the memory leak in the worker pool"

# Get JSON output for scripting
alcove run --output json "Update the README" | jq -r '.id'
```

---

## alcove list

List sessions with optional filters.

```
alcove list [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--status` | string | Filter by status: `running`, `completed`, `error`, `cancelled`, `timeout` |
| `--repo` | string | Filter by repository |
| `--since` | duration | Show sessions from the last duration (e.g., `24h`, `168h`) |

### Description

Retrieves sessions from the Bridge and displays them in a table. Each row shows
the session ID, status, repository, provider, duration, and a truncated prompt.

### Examples

```bash
# List all sessions
alcove list

# Show only running sessions
alcove list --status running

# Sessions for a specific repo in the last 24 hours
alcove list --repo myorg/myapp --since 24h

# JSON output for scripting
alcove list --output json | jq '.sessions[] | select(.status == "completed")'
```

---

## alcove logs

Fetch or stream session logs (transcript or proxy log).

```
alcove logs [session-id] [flags]
```

### Flags

| Flag | Short | Type | Description |
|------|-------|------|-------------|
| `--follow` | `-f` | bool | Stream logs in real time via SSE |
| `--proxy` | | bool | Show the Gate proxy log instead of the session transcript |
| `--denied` | | bool | Show only denied proxy requests (useful for debugging scope issues) |

### Description

By default, fetches the complete transcript for a finished session. Use `-f` to
stream logs from a running session in real time. The `--proxy` flag switches to
the Gate proxy log, which records every HTTP request the agent made. Combine
`--proxy` with `--denied` to see only requests that Gate blocked.

### Examples

```bash
# Fetch the full transcript of a completed session
alcove logs abc123

# Stream a running session's transcript
alcove logs -f abc123

# View the proxy log to see what requests the agent made
alcove logs --proxy abc123

# Show only denied requests (scope violations)
alcove logs --proxy --denied abc123
```

---

## alcove status

Show detailed status for a single session.

```
alcove status [session-id]
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Retrieves full session details including status, provider, repository, timing,
exit code, and any artifacts (such as branches pushed or pull requests created).

### Examples

```bash
# View session details
alcove status abc123

# JSON output
alcove status --output json abc123
```

Sample table output:

```
Session:    abc123
Status:     completed
Provider:   anthropic
Repository: myorg/myapp
Started:    2026-03-25T10:00:00Z
Finished:   2026-03-25T10:12:34Z
Duration:   12m34s
Exit Code:  0
Prompt:     Fix the failing test in pkg/auth

Artifacts:
  [branch] fix/auth-test
  [pull_request] https://github.com/myorg/myapp/pull/42
```

---

## alcove cancel

Cancel a running session.

```
alcove cancel [session-id]
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Sends a cancellation request to the Bridge, which terminates the Skiff container
and marks the session as cancelled. The cancellation is asynchronous -- the
command returns immediately after the request is accepted.

### Examples

```bash
# Cancel a running session
alcove cancel abc123

# Cancel and confirm with JSON output
alcove cancel --output json abc123
```

---

## alcove login

Authenticate to a Bridge instance.

```
alcove login [bridge-url]
```

### Flags

No command-specific flags.

### Description

Prompts for a username and password, authenticates against the Bridge, and saves
the server URL and authentication token locally. Configuration is stored in
`~/.config/alcove/` (or `$XDG_CONFIG_HOME/alcove/`):

- `config.yaml` -- server URL
- `credentials` -- authentication token

Both files are created with `0600` permissions.

### Examples

```bash
# Log in to a Bridge instance
alcove login https://alcove.example.com

# Log in to a local development Bridge
alcove login http://localhost:8080
```

---

## alcove config validate

Check the current CLI configuration for issues.

```
alcove config validate
```

### Flags

No command-specific flags.

### Description

Validates that the config file and credentials are present and well-formed.
Reports the configured server URL, token status, and whether `ALCOVE_SERVER`
is set in the environment. Exits with a non-zero status if any issues are found.

### Examples

```bash
# Validate configuration
alcove config validate
```

Sample output when valid:

```
config: server = https://alcove.example.com
credentials: token present (128 chars)

Configuration is valid.
```

Sample output with issues:

```
config: server = https://alcove.example.com
credentials: cannot read /home/user/.config/alcove/credentials: no such file

Issues:
  - credentials: cannot read /home/user/.config/alcove/credentials: no such file
Error: configuration has 1 issue(s)
```

---

## alcove version

Print the CLI version.

```
alcove version
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Prints the client version. The version is set at build time via `-ldflags`;
development builds report `dev`.

### Examples

```bash
# Print version
alcove version

# JSON output
alcove version --output json
```
