# Alcove CLI Reference

The `alcove` CLI dispatches and manages AI coding tasks via the Bridge API.

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

## Configuration File

The CLI supports configuration files to set default values for common options:

```bash
# Create a config file with all options documented
alcove config init

# Show your current effective configuration
alcove config show

# Validate your configuration
alcove config validate
```

### Configuration Locations

Config files are searched in this order:

1. `~/.config/alcove/config.yaml` (XDG standard)
2. `~/.alcove.yaml` (convenience location)
3. `$XDG_CONFIG_HOME/alcove/config.yaml` (if `XDG_CONFIG_HOME` is set)

### Configuration Precedence

Settings are resolved in this order (highest to lowest priority):

1. **Command-line flags** (e.g., `--provider anthropic`)
2. **Environment variables** (e.g., `ALCOVE_SERVER`)  
3. **Config file values** (e.g., `provider: anthropic` in config.yaml)
4. **Built-in defaults**

### Example Configuration

```yaml
# Alcove CLI Configuration
server: https://bridge.example.com
provider: anthropic
model: claude-sonnet-4-20250514
budget: 5.00
timeout: 30m
output: table
repo: myorg/myproject
```

## Global Flags

| Flag | Description |
|------|-------------|
| `--server <url>` | Bridge server URL (overrides env and config file) |
| `--output <format>` | Output format: `json` or `table` (default: `table`) |
| `--proxy-url <url>` | HTTP/HTTPS proxy URL (overrides environment) |
| `--no-proxy <hosts>` | Comma-separated list of hosts to exclude from proxy (overrides `NO_PROXY` env var) |
| `-u, --username <user>` | Username for Basic Auth (overrides `ALCOVE_USERNAME`) |
| `-p, --password <pass>` | Password for Basic Auth (overrides `ALCOVE_PASSWORD`) |

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

1. CLI flags (`--proxy-url`, `--no-proxy`) — highest priority
2. Environment variables (`HTTPS_PROXY`/`HTTP_PROXY`, `NO_PROXY`)
3. No proxy (default)

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

All flags can be set as defaults in your config file. Use `alcove config init`
to create an example config file, then edit it to set your preferred defaults
for provider, model, budget, timeout, and repository.

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

# Using config file defaults (with config.yaml containing provider: anthropic)
alcove run "Implement feature X"  # Uses provider from config file
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

## alcove config

Configuration management for the CLI.

```
alcove config [subcommand]
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `init` | Create an example configuration file |
| `show` | Show current effective configuration |
| `validate` | Validate the current configuration |

---

### alcove config init

Create an example configuration file with all supported options.

```
alcove config init
```

#### Description

Creates `~/.config/alcove/config.yaml` with an example configuration that includes
all supported options with helpful comments. Will not overwrite an existing config file.

#### Examples

```bash
# Create initial config file
alcove config init

# Edit the file to set your defaults
$EDITOR ~/.config/alcove/config.yaml
```

---

### alcove config show

Show the current effective configuration after applying all precedence rules.

```
alcove config show
```

#### Description

Displays the effective configuration values, showing the source of each setting
(flag, environment variable, config file, or default). Useful for debugging
configuration precedence.

#### Examples

```bash
# Show configuration in table format
alcove config show

# Show configuration in JSON format  
alcove config show --output json
```

Sample output:

```
Current effective configuration:
(showing resolved values after applying precedence: flag > env > config > default)

Server:   https://bridge.example.com (from config file)
Provider: anthropic (from config file)
Model:    claude-sonnet-4-20250514 (from config file)
Budget:   5.00 (from config file)
Timeout:  30m0s (from config file)
Output:   table (default)
Repo:     myorg/myproject (from config file)
```

---

### alcove config validate

Check the current CLI configuration for issues.

```
alcove config validate
```

#### Description

Validates that the config file and credentials are present and well-formed.
Reports the configured server URL, token status, and whether `ALCOVE_SERVER`
is set in the environment. Also validates config field values (e.g., output format,
timeout format, budget constraints). Exits with a non-zero status if any issues are found.

#### Examples

```bash
# Validate configuration
alcove config validate
```

Sample output when valid:

```
config: server = https://alcove.example.com
config: found configuration with 7 fields
credentials: token present (128 chars)

Configuration is valid.
```

Sample output with issues:

```
config: server = https://alcove.example.com
config: found configuration with 3 fields

Issues:
  - credentials: cannot read /home/user/.config/alcove/credentials: no such file
  - config: invalid output format 'xml' (must be 'json' or 'table')
  - config: invalid timeout format 'invalid': time: invalid duration "invalid"
Error: configuration has 3 issue(s)
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
