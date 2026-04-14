# Alcove CLI Reference

The `alcove` CLI dispatches and manages AI coding sessions via the Bridge API.

```
alcove [command] [flags]
```

## Installation

### One-Line Install (Linux/macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | bash
```

### Windows (PowerShell)

```powershell
iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
```

### Manual Download

Download platform-specific binaries from [GitHub Releases](https://github.com/bmbouter/alcove/releases/latest):

- **Linux AMD64**: `alcove-linux-amd64`
- **Linux ARM64**: `alcove-linux-arm64`  
- **macOS Intel**: `alcove-darwin-amd64`
- **macOS Apple Silicon**: `alcove-darwin-arm64`
- **Windows**: `alcove-windows-amd64.exe`

### Custom Installation Directory

```bash
# Linux/macOS with custom directory
curl -fsSL https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.sh | INSTALL_DIR="$HOME/bin" bash

# Windows with custom directory
$env:INSTALL_DIR = "C:\tools"; iex (iwr -useb 'https://raw.githubusercontent.com/bmbouter/alcove/main/scripts/install.ps1').Content
```

### Verify Installation

```bash
alcove version
alcove --help
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--server <url>` | Bridge server URL (overrides env and config file) |
| `--output <format>` | Output format: `json` or `table` (default: `table`) |
| `--profile <name>` | Use a named profile from config (overrides `ALCOVE_PROFILE` and `active_profile`) |
| `--proxy-url <url>` | HTTP/HTTPS proxy URL (overrides environment) |
| `--no-proxy <hosts>` | Comma-separated list of hosts to exclude from proxy (overrides `NO_PROXY` env var) |
| `-u, --username <user>` | Username for Basic Auth (overrides `ALCOVE_USERNAME`) |
| `-p, --password <pass>` | Password for Basic Auth (overrides `ALCOVE_PASSWORD`) |

### Authentication Methods

The CLI supports three authentication methods:

1. **Token-based authentication** (default): Authenticates with stored token from `alcove login`
2. **Basic Auth with password**: Uses username/password (set via flags, environment variables, or config file)
3. **Basic Auth with personal API token**: Uses username and personal API token instead of password

Basic Auth takes precedence over token-based authentication when provided.

#### Personal API Tokens

For postgres auth backend, you can use personal API tokens instead of passwords:

```bash
# Create a token via the web dashboard, then configure CLI
alcove config set server https://alcove.example.com
alcove config set username admin
alcove config set password apat_a1b2c3d4e5f6789012345678901234567890

# Now use normally
alcove list
alcove run "Fix the failing tests"
```

Personal API tokens:
- Start with `apat_` prefix
- Never expire (user revokes manually)
- Have same permissions as the user
- Are more secure than passwords for CLI/API usage

## Environment Variables

The Alcove CLI respects several environment variables for configuration:

| Variable | Description |
|----------|-------------|
| `ALCOVE_SERVER` | Bridge server URL |
| `ALCOVE_OUTPUT` | Output format: `json` or `table` |
| `ALCOVE_PROFILE` | Named profile to use (overrides `active_profile` in config) |
| `ALCOVE_USERNAME` | Username for Basic Auth |
| `ALCOVE_PASSWORD` | Password for Basic Auth |
| `HTTP_PROXY` | HTTP proxy URL for API requests |
| `HTTPS_PROXY` | HTTPS proxy URL for API requests (takes precedence) |
| `NO_PROXY` | Comma-separated list of hosts to exclude from proxy |
| `http_proxy` | Alternative lowercase version of `HTTP_PROXY` |
| `https_proxy` | Alternative lowercase version of `HTTPS_PROXY` |
| `no_proxy` | Alternative lowercase version of `NO_PROXY` |
| `XDG_CONFIG_HOME` | Override for config directory (default: `~/.config`) |

---

## Proxy Configuration

The Alcove CLI supports HTTP and HTTPS proxies for connecting to Bridge instances
behind corporate proxies. Proxy configuration follows standard conventions and
supports both environment variables and command-line flags.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `HTTP_PROXY` | HTTP proxy URL for API requests |
| `HTTPS_PROXY` | HTTPS proxy URL for API requests (takes precedence over `HTTP_PROXY`) |
| `NO_PROXY` | Comma-separated list of hosts to exclude from proxy |
| `http_proxy` | Alternative lowercase version of `HTTP_PROXY` |
| `https_proxy` | Alternative lowercase version of `HTTPS_PROXY` |
| `no_proxy` | Alternative lowercase version of `NO_PROXY` |

### Configuration Precedence

1. CLI flags (`--proxy-url`, `--no-proxy`) -- highest priority
2. Environment variables (`HTTPS_PROXY`/`https_proxy`, `HTTP_PROXY`/`http_proxy`, `NO_PROXY`/`no_proxy`)
3. Config file (`~/.config/alcove/config.yaml`)
4. No proxy (default)

### Proxy URL Format

Proxy URLs must use `http://` or `https://` schemes and support authentication:

```
http://proxy.example.com:8080
https://proxy.example.com:443
http://username:password@proxy.example.com:8080
```

### NO_PROXY Exclusions

The `NO_PROXY` environment variable or `--no-proxy` flag supports:

- **Exact host match**: `example.com`
- **Host with port**: `example.com:8080`
- **Domain suffix**: `.example.com` (matches `api.example.com`)
- **Wildcard domain**: `*.example.com` (matches `api.example.com`)
- **IP addresses**: `192.168.1.1`
- **CIDR ranges**: `192.168.1.0/24`
- **Port-only**: `8080` (excludes any host on port 8080)

### Examples

```bash
# Use environment variables
export HTTP_PROXY=http://proxy.company.com:8080
export NO_PROXY=localhost,127.0.0.1,.internal.com
alcove list

# Override with CLI flags
alcove --proxy-url=http://proxy.company.com:8080 --no-proxy=localhost list

# Authenticated proxy
export HTTP_PROXY=http://user:password@proxy.company.com:8080
alcove run "Fix the bug"

# Corporate proxy with exclusions
export HTTPS_PROXY=https://corporate.proxy:3128
export NO_PROXY=.internal.company.com,192.168.0.0/16
alcove login https://alcove.internal.company.com
```

### Troubleshooting

- **Connection failures**: Verify proxy URL format and credentials
- **Certificate issues**: Corporate proxies may require custom CA certificates
- **Scope violations**: Use `--no-proxy` to exclude internal Bridge instances
- **Debugging**: Check proxy logs and use `alcove config validate` to verify configuration
- **Timeouts**: The CLI uses a 30-second timeout for all HTTP requests
- **SSL/TLS**: For HTTPS servers with custom certificates, ensure the system's certificate store is configured correctly

## Server Resolution

The CLI resolves the Bridge server URL using the following precedence:

1. `--server` flag (highest priority)
2. `ALCOVE_SERVER` environment variable
3. Config file at `~/.config/alcove/config.yaml` (set by `alcove login`)

If none are configured, commands that contact the Bridge will fail with an error.

## Output Formatting

The Alcove CLI supports two output formats:

- **table** (default): Human-readable tabular format
- **json**: Machine-readable JSON format for scripting

Output format can be controlled via:
1. `--output` flag (highest priority)
2. `ALCOVE_OUTPUT` environment variable
3. Config file `output` setting
4. Default: `table`

### JSON Output Examples

```bash
# Get session ID from run command for scripting
SESSION_ID=$(alcove run --output json "Fix the bug" | jq -r '.id')

# Filter sessions programmatically
alcove list --output json | jq '.sessions[] | select(.status == "completed")'

# Get version information
alcove version --output json | jq -r '.version'
```

## Configuration File

Alcove CLI reads configuration from `~/.config/alcove/config.yaml` (or
`$XDG_CONFIG_HOME/alcove/config.yaml`). Settings in the config file are used as
defaults when flags and environment variables are not set.

### Example config.yaml

```yaml
server: https://alcove.example.com
output: table
username: myuser
password: mypassword
proxy_url: http://proxy.example.com:8080
no_proxy: localhost,127.0.0.1,.internal.com
defaults:
  repo: https://github.com/myorg/myrepo.git
  provider: google-vertex
  model: claude-sonnet-4-20250514
  timeout: 30m
  budget: 5.00
```

### Named Profiles

You can configure multiple Alcove installations as named profiles. This is
useful when you work with different environments (staging, production, local
dev).

```yaml
# Which profile is active by default
active_profile: hcmai

# Named profiles
profiles:
  crc:
    server: https://internal.console.stage.redhat.com/app/alcove
    username: "13409664|alcove-dev"
    password: "apat_abc123..."
    proxy_url: http://squid.corp.redhat.com:3128
  hcmai:
    server: https://alcove-bridge-pulp-stage.apps.rosa.hcmais01ue1.s9m2.p3.openshiftapps.com
    username: admin
    password: "apat_def456..."
  local:
    server: http://localhost:8080
    username: admin
    password: admin

# Top-level fields still work for backward compat (treated as "default" profile)
server: http://localhost:8080
output: table
defaults:
  timeout: 30m
```

Profile resolution order:
1. `--profile` flag (highest priority)
2. `ALCOVE_PROFILE` environment variable
3. `active_profile` setting in config file
4. Top-level (inline) fields as the default profile

Existing config files without profiles continue to work unchanged.

### Priority Order (highest to lowest)

1. CLI flags (e.g., `--server`, `--repo`)
2. Environment variables (e.g., `ALCOVE_SERVER`, `ALCOVE_OUTPUT`)
3. Active profile from config file (`~/.config/alcove/config.yaml`)
4. Built-in defaults

### Managing Configuration

```bash
# Show current configuration
alcove config show

# Set individual values (operates on active profile)
alcove config set server https://alcove.example.com
alcove config set defaults.repo https://github.com/myorg/myrepo.git
alcove config set defaults.timeout 30m
alcove config set defaults.budget 5.00

# Set values on a specific profile
alcove --profile crc config set username admin

# Validate configuration
alcove config validate
```

---

## alcove run

Start a coding session on the Bridge for execution in an ephemeral Skiff container.

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
| `--timeout` | duration | Session timeout (e.g., `30m`, `1h`) |
| `--watch` | bool | Stream the session transcript via SSE after dispatch |
| `--debug` | bool | Keep containers after exit for log inspection |

### Description

Starts a session on the Bridge, which launches a Skiff
container. By default, the command prints the session ID and exits immediately.
With `--watch`, it streams the live transcript until the session completes.

### Examples

```bash
# Start a session and get the session ID
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

# Stream both transcript and follow a running session
alcove logs -f abc123

# For large buffer sizes with SSE streaming, the CLI handles up to 1MB message buffers
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

## alcove delete

Delete completed, errored, timed-out, or cancelled sessions.

```
alcove delete [session-id] [flags]
```

### Flags

- `--status string`: Delete sessions with specific status (`completed`, `error`, `timeout`, `cancelled`)
- `--before string`: Delete sessions finished before date/time (RFC3339) or duration (e.g., `7d`, `30d`)
- `--dry-run`: Show what would be deleted without actually deleting

### Description

Permanently deletes session records, transcripts, and proxy logs from the database. 
Only sessions in terminal states can be deleted -- running sessions must be cancelled first.

For single session deletion, provide the session ID as an argument. For bulk deletion, 
use the `--status` and/or `--before` flags to filter sessions to delete.

The `--dry-run` flag is useful for previewing bulk deletions before executing them.

### Examples

```bash
# Delete a specific session
alcove delete 12345678-abcd-1234-abcd-123456789012

# Delete all error sessions older than 7 days
alcove delete --status error --before 7d

# Delete all completed sessions before a specific date
alcove delete --status completed --before 2023-01-01T00:00:00Z

# Dry run to see what would be deleted (bulk only)
alcove delete --status error --before 30d --dry-run

# Delete all cancelled sessions
alcove delete --status cancelled

# Delete all sessions older than 90 days regardless of status
alcove delete --before 90d
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

**Note**: The `login` command cannot be used with Basic Auth flags (`--username`/`--password`) or 
environment variables (`ALCOVE_USERNAME`/`ALCOVE_PASSWORD`) as it creates a token-based 
authentication session. Use either token-based auth (via `login`) or Basic Auth, but not both.

### Examples

```bash
# Log in to a Bridge instance
alcove login https://alcove.example.com

# Log in to a local development Bridge
alcove login http://localhost:8080
```

---

## alcove config show

Show the current configuration file contents.

```
alcove config show
```

### Flags

No command-specific flags.

### Description

Displays the parsed contents of the config file at `~/.config/alcove/config.yaml`.
If no config file exists, prints a message to stderr indicating the expected path.

### Examples

```bash
# Show current config
alcove config show
```

Sample output:

```yaml
server: https://alcove.example.com
output: table
defaults:
  repo: https://github.com/myorg/myrepo.git
  provider: google-vertex
  timeout: 30m
  budget: 5
```

---

## alcove config set

Set a configuration value.

```
alcove config set <key> <value>
```

### Flags

No command-specific flags.

### Description

Sets a single configuration key in the config file. Creates the config file and
directory if they don't exist. Valid keys:

| Key | Description |
|-----|-------------|
| `server` | Bridge server URL |
| `output` | Output format (`json` or `table`) |
| `username` | Basic Auth username |
| `password` | Basic Auth password |
| `proxy_url` | HTTP/HTTPS proxy URL |
| `no_proxy` | Comma-separated no-proxy hosts |
| `defaults.repo` | Default repository |
| `defaults.provider` | Default LLM provider |
| `defaults.model` | Default model |
| `defaults.timeout` | Default timeout (e.g., `30m`, `1h`) |
| `defaults.budget` | Default budget in USD |

### Examples

```bash
# Set the default server
alcove config set server https://alcove.example.com

# Set default repository and provider
alcove config set defaults.repo https://github.com/myorg/myrepo.git
alcove config set defaults.provider google-vertex

# Set a budget limit
alcove config set defaults.budget 5.00

# Set a timeout
alcove config set defaults.timeout 30m
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

## alcove profile list

List all named profiles from the config file.

```
alcove profile list
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all configured profiles with their server URLs. The active profile is
marked with an asterisk (`*`).

### Examples

```bash
# List all profiles
alcove profile list
```

Sample output:

```
  crc     https://internal.console.stage.redhat.com/app/alcove
* hcmai   https://alcove-bridge-pulp-stage.apps.rosa.hcmais01ue1.s9m2.p3.openshiftapps.com
  local   http://localhost:8080
```

---

## alcove profile use

Set the active profile.

```
alcove profile use <name>
```

### Flags

No command-specific flags.

### Description

Sets `active_profile` in the config file. All subsequent commands will use this
profile's settings (server, credentials, proxy, defaults) unless overridden by
flags, environment variables, or `--profile`.

### Examples

```bash
# Switch to the CRC profile
alcove profile use crc

# Switch to local development
alcove profile use local
```

---

## alcove profile add

Create a new named profile.

```
alcove profile add <name> [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--server` | string | Bridge server URL |
| `--username` | string | Username for Basic Auth |
| `--password` | string | Password for Basic Auth |
| `--proxy-url` | string | HTTP/HTTPS proxy URL |
| `--no-proxy` | string | Comma-separated no-proxy hosts |

### Description

Creates a new named profile in the config file. The profile name must not
already exist. Use `alcove config set` to modify an existing profile, or
`alcove profile remove` and re-add to replace one.

### Examples

```bash
# Add a profile with just a server URL
alcove profile add staging --server https://staging.example.com

# Add a profile with credentials
alcove profile add production --server https://prod.example.com --username admin --password secret

# Add a profile with proxy
alcove profile add corp --server https://internal.example.com --proxy-url http://proxy:3128
```

---

## alcove profile remove

Delete a named profile.

```
alcove profile remove <name>
```

### Flags

No command-specific flags.

### Description

Removes a named profile from the config file. If the removed profile was the
active profile, `active_profile` is cleared.

### Examples

```bash
# Remove a profile
alcove profile remove staging
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
