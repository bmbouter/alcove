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
| `--team <name>` | Team name to scope this invocation (overrides `active_team` in profile) |
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
active_team: my-team
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
| `active_team` | Active team name for team-scoped operations |
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

---

## alcove teams

Manage teams. Every resource in Alcove belongs to a team. Users can belong to
multiple teams, and a personal team is auto-created on signup.

```
alcove teams <subcommand>
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all teams |
| `create` | Create a new team |
| `use` | Set the active team for the current profile |
| `add-member` | Add a member to a team |
| `remove-member` | Remove a member from a team |
| `delete` | Delete a team |

---

## alcove teams list

List all teams the current user belongs to.

```
alcove teams list
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all teams in a table. The active team (set via `alcove teams use` or
`active_team` in the config file) is marked with an asterisk (`*`). Each row
shows the team name, whether it is a personal team, and the creation timestamp.

### Examples

```bash
# List all teams
alcove teams list

# JSON output
alcove teams list --output json
```

Sample table output:

```
  NAME        PERSONAL  CREATED
* my-team     no        2026-03-25T10:00:00Z
  personal    yes       2026-03-20T08:00:00Z
```

---

## alcove teams create

Create a new team.

```
alcove teams create <name>
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Creates a new team with the given name. The current user is automatically added
as a member. After creating a team, use `alcove teams use` to set it as the
active team.

### Examples

```bash
# Create a team
alcove teams create backend-team

# Create and get JSON output
alcove teams create --output json frontend-team
```

---

## alcove teams use

Set the active team for the current profile.

```
alcove teams use [name]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--personal` | bool | Switch to your personal team |

### Description

Sets `active_team` in the config file for the active profile. All subsequent
team-scoped commands (sessions, catalog, credentials) will use this team unless
overridden by the `--team` global flag.

Use `--personal` to switch to your auto-created personal team without needing to
know its name.

### Examples

```bash
# Set active team by name
alcove teams use backend-team

# Switch to personal team
alcove teams use --personal

# Set active team for a specific profile
alcove --profile staging teams use backend-team
```

---

## alcove teams add-member

Add a member to a team.

```
alcove teams add-member <team> <username>
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Adds the specified user to the team. The team name is resolved to its ID
automatically.

### Examples

```bash
# Add a user to a team
alcove teams add-member backend-team alice

# JSON output
alcove teams add-member --output json backend-team bob
```

---

## alcove teams remove-member

Remove a member from a team.

```
alcove teams remove-member <team> <username>
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Removes the specified user from the team.

### Examples

```bash
# Remove a user from a team
alcove teams remove-member backend-team alice
```

---

## alcove teams delete

Delete a team.

```
alcove teams delete <team> [flags]
```

### Flags

| Flag | Short | Type | Description |
|------|-------|------|-------------|
| `--yes` | `-y` | bool | Skip the confirmation prompt |

### Description

Permanently deletes a team. This cannot be undone. Without `-y`, prompts for
confirmation before proceeding.

### Examples

```bash
# Delete a team (prompts for confirmation)
alcove teams delete old-team

# Delete without confirmation prompt
alcove teams delete -y old-team
```

---

## alcove catalog

Manage the service catalog. The catalog contains sources (git repos) and items
(agents, plugins, MCPs) that can be enabled or disabled per team.

```
alcove catalog <subcommand>
```

Catalog commands require an active team. Set one with `alcove teams use <name>`
or pass `--team <name>` on each invocation.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List catalog entries |
| `search` | Search catalog entries by name, description, or tags |
| `items` | List items within a catalog source |
| `enable` | Enable a catalog source or individual item |
| `disable` | Disable a catalog source or individual item |
| `agents` | List all enabled agents across all sources |

---

## alcove catalog list

List catalog entries for the active team.

```
alcove catalog list [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--category` | string | Filter by category |

### Description

Displays all catalog entries in a table. Each row shows the entry ID, category,
source type, name, enabled status, and a truncated description.

### Examples

```bash
# List all catalog entries
alcove catalog list

# Filter by category
alcove catalog list --category agent

# JSON output
alcove catalog list --output json

# List entries for a specific team (without switching active team)
alcove --team backend-team catalog list
```

---

## alcove catalog search

Search catalog entries by name, description, or tags.

```
alcove catalog search <query>
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Searches all catalog entries for the active team. The query is matched
case-insensitively against the entry name, description, and tags. Results are
displayed in the same table format as `alcove catalog list`.

### Examples

```bash
# Search for entries related to "marketing"
alcove catalog search marketing

# Search with JSON output
alcove catalog search --output json testing
```

---

## alcove catalog items

List items within a catalog source.

```
alcove catalog items <source> [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--search` | string | Filter items by name or slug |

### Description

Displays items (agents, plugins, MCPs) that belong to a specific catalog source.
Each row shows the slug, name, type, and enabled status.

### Examples

```bash
# List all items in a source
alcove catalog items agency-agents

# Filter items by name
alcove catalog items agency-agents --search writer

# JSON output
alcove catalog items --output json agency-agents
```

---

## alcove catalog enable

Enable a catalog source or individual item for the active team.

```
alcove catalog enable <source>[/<item>]
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Enables catalog entries so they are available for use in sessions. When the
argument contains a `/`, it enables a single item within a source. When no `/`
is present, it enables all items in the source (bulk enable).

### Examples

```bash
# Enable a single item
alcove catalog enable agency-agents/marketing-writer

# Enable all items in a source
alcove catalog enable agency-agents

# Enable for a specific team
alcove --team backend-team catalog enable agency-agents/code-reviewer
```

---

## alcove catalog disable

Disable a catalog source or individual item for the active team.

```
alcove catalog disable <source>[/<item>]
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Disables catalog entries so they are no longer available for use in sessions.
When the argument contains a `/`, it disables a single item within a source.
When no `/` is present, it disables all items in the source (bulk disable).

### Examples

```bash
# Disable a single item
alcove catalog disable agency-agents/marketing-writer

# Disable all items in a source
alcove catalog disable agency-agents
```

---

## alcove catalog agents

List all enabled agents across all catalog sources.

```
alcove catalog agents
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all agents that are currently enabled for the active team, aggregated
across all catalog sources. Each row shows the `source/slug` identifier and the
agent name. These identifiers are used to reference agents in workflow step
definitions.

### Examples

```bash
# List all enabled agents
alcove catalog agents

# JSON output
alcove catalog agents --output json
```

Sample table output:

```
SOURCE/SLUG                          NAME
agency-agents/marketing-writer       Marketing Writer
agency-agents/code-reviewer          Code Reviewer
```

---

## alcove credentials

Manage team credentials and secrets. Credentials store API keys, service account
JSON, and other secrets used by Gate to proxy requests to external services.

```
alcove credentials <subcommand>
```

The command is also available via the `creds` alias: `alcove creds <subcommand>`.

Credential commands require an active team. Set one with `alcove teams use <name>`
or pass `--team <name>` on each invocation.

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List credentials for the active team |
| `create` | Create a new credential |
| `delete` | Delete a credential by ID |

---

## alcove credentials list

List credentials for the active team.

```
alcove credentials list
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all credentials for the active team. Each row shows the credential
name, provider, auth type, and creation timestamp. Credential values (secrets)
are never displayed.

### Examples

```bash
# List credentials
alcove credentials list

# Using the alias
alcove creds list

# JSON output
alcove credentials list --output json
```

Sample table output:

```
NAME             PROVIDER        TYPE             CREATED
anthropic-key    anthropic       api_key          2026-03-25T10:00:00Z
github-pat       github          secret           2026-03-20T08:00:00Z
vertex-sa        google-vertex   service_account  2026-03-18T14:30:00Z
```

---

## alcove credentials create

Create a new credential for the active team.

```
alcove credentials create [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--name` | string | Credential name (required) |
| `--provider` | string | Provider name (default: `generic`). Examples: `anthropic`, `google-vertex`, `github`, `gitlab` |
| `--auth-type` | string | Auth type (default: `secret`). Examples: `api_key`, `service_account`, `secret` |
| `--secret` | string | Shorthand: sets `provider=generic`, `auth-type=secret`, and uses value as credential |
| `--credential` | string | Credential value (e.g., API key, service account JSON) |
| `--project-id` | string | GCP project ID (Vertex AI only) |
| `--region` | string | GCP region (Vertex AI only) |
| `--api-host` | string | Custom API host (e.g., self-hosted GitLab URL) |

### Description

Creates a new credential for the active team. Either `--secret` (shorthand) or
`--credential` must be provided along with `--name`.

The `--secret` flag is a convenience shorthand that sets the provider to
`generic` and the auth type to `secret`. For provider-specific credentials (LLM
API keys, SCM tokens), use `--provider` and `--auth-type` with `--credential`.

### Examples

```bash
# Create a generic secret
alcove credentials create --name my-token --secret sk-abc123

# Create an Anthropic API key credential
alcove credentials create --name anthropic-key --provider anthropic --auth-type api_key --credential sk-ant-abc123

# Create a Google Vertex AI service account credential
alcove credentials create --name vertex-sa --provider google-vertex --auth-type service_account \
  --credential '{"type":"service_account",...}' --project-id my-project --region us-east5

# Create a GitHub PAT credential
alcove credentials create --name github-pat --provider github --auth-type secret --credential ghp_abc123

# Create a credential for a self-hosted GitLab instance
alcove credentials create --name gitlab-token --provider gitlab --auth-type secret \
  --credential glpat-abc123 --api-host https://gitlab.company.com

# Using the alias
alcove creds create --name my-secret --secret supersecretvalue
```

---

## alcove credentials delete

Delete a credential by ID.

```
alcove credentials delete <id>
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Permanently deletes a credential. Use `alcove credentials list` to find the
credential ID.

### Examples

```bash
# Delete a credential
alcove credentials delete 12345678-abcd-1234-abcd-123456789012

# JSON output
alcove credentials delete --output json 12345678-abcd-1234-abcd-123456789012
```

---

## alcove agents

Manage agent definitions and agent repos. Agent definitions are synced from
YAML files in registered agent repos (`.alcove/agents/*.yml`). Use these
commands to list, sync, run agents, and manage the repos they come from.

```
alcove agents <subcommand>
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List synced agent definitions |
| `sync` | Trigger agent definition sync |
| `run` | Run an agent definition by name |
| `repos` | List and manage configured agent repos |

---

## alcove agents list

List synced agent definitions for the active team.

```
alcove agents list
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all agent definitions that have been synced from registered agent
repos. Each row shows the agent name, source repo, and other metadata.

### Examples

```bash
# List all agent definitions
alcove agents list

# JSON output
alcove agents list --output json
```

---

## alcove agents sync

Trigger an immediate agent definition sync.

```
alcove agents sync
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Requests the Bridge to sync agent definitions from all registered agent repos
immediately, rather than waiting for the next automatic sync interval
(default: 15 minutes). This is equivalent to clicking "Sync Now" in the
dashboard.

### Examples

```bash
# Trigger a sync
alcove agents sync
```

---

## alcove agents run

Run an agent definition by name.

```
alcove agents run <name> [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--watch` | bool | Stream the session transcript via SSE after dispatch |

### Description

Dispatches a session using a named agent definition. The agent definition must
have been synced from an agent repo. The agent's prompt, repos, provider,
model, timeout, budget, profiles, and tools are all taken from the definition.

With `--watch`, the CLI streams the live transcript until the session completes
(same behavior as `alcove run --watch`).

### Examples

```bash
# Run an agent by name
alcove agents run run-tests

# Run and stream the transcript
alcove agents run --watch run-tests

# JSON output
alcove agents run --output json run-tests
```

---

## alcove agents repos

List configured agent repos for the active team.

```
alcove agents repos
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `add` | Add an agent repo |
| `remove` | Remove an agent repo |

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--json` | bool | Output JSON instead of table format |

Also supports global `--output json`.

### Description

When run without a subcommand, lists all agent repos configured for the active
team. Each row shows the repo name, URL, and ref (branch/tag).

### Examples

```bash
# List agent repos
alcove agents repos

# JSON output using dedicated flag
alcove agents repos --json

# JSON output using global flag
alcove agents repos --output json
```

---

## alcove agents repos add

Add an agent repo.

```
alcove agents repos add [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--url` | string | Repository URL (required) |
| `--ref` | string | Branch, tag, or commit (default: `main`) |
| `--name` | string | Display name for the repo |

### Description

Registers a new agent repo for the active team. Bridge will sync agent
definitions from `.alcove/agents/*.yml` in the repo on the next sync cycle.

### Examples

```bash
# Add a repo with default branch
alcove agents repos add --url https://github.com/org/my-agents.git

# Add a repo with a specific branch and display name
alcove agents repos add --url https://github.com/org/my-agents.git --ref develop --name "My Agents"
```

---

## alcove agents repos remove

Remove an agent repo.

```
alcove agents repos remove [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--url` | string | Repository URL to remove |
| `--name` | string | Name of the repo to remove |

### Description

Removes an agent repo from the active team. Provide either `--url` or `--name`
to identify the repo to remove. Agent definitions previously synced from this
repo will be removed on the next sync.

### Examples

```bash
# Remove a repo by URL
alcove agents repos remove --url https://github.com/org/my-agents.git

# Remove a repo by name
alcove agents repos remove --name "My Agents"
```

---

## alcove workflows

Manage workflows and workflow runs. Workflows are multi-step execution graphs
defined in YAML and synced from agent repos.

```
alcove workflows <subcommand>
```

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all workflow definitions |
| `run` | Trigger a workflow run by ID or name |
| `runs` | List workflow runs |
| `cancel` | Cancel a workflow run |

---

## alcove workflows list

List all workflow definitions for the active team.

```
alcove workflows list
```

### Flags

No command-specific flags. Supports global `--output json`.

### Description

Displays all workflow definitions in a table. Each row shows the workflow ID,
name, source repo, last sync time, and any sync errors.

### Examples

```bash
# List all workflows
alcove workflows list

# JSON output
alcove workflows list --output json
```

---

## alcove workflows run

Trigger a workflow run manually.

```
alcove workflows run <id-or-name> [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--trigger-ref` | string | Optional trigger reference (e.g., branch name, PR number) |

### Description

Starts a new workflow run by workflow ID or name. If a name is provided, it is
resolved to a workflow ID by listing all workflows and matching by name. On
success, prints the workflow run ID to stdout.

### Examples

```bash
# Trigger by name
alcove workflows run "Pulp Dependency Upgrade Pipeline"

# Trigger by ID
alcove workflows run b1c2d3e4-f5a6-7890-abcd-ef1234567890

# With a trigger reference
alcove workflows run "My Workflow" --trigger-ref "feature/new-api"

# JSON output
alcove workflows run --output json "My Workflow"
```

---

## alcove workflows runs

List workflow runs for the active team.

```
alcove workflows runs [flags]
```

### Flags

| Flag | Type | Description |
|------|------|-------------|
| `--status` | string | Filter by status: `pending`, `running`, `completed`, `failed`, `cancelled`, `awaiting_approval` |

### Description

Displays workflow runs in a table. Each row shows the run ID, workflow ID,
status, trigger type, current step, and creation time.

### Examples

```bash
# List all workflow runs
alcove workflows runs

# Filter by status
alcove workflows runs --status running

# JSON output
alcove workflows runs --output json
```

----

## alcove workflows cancel

Cancel a workflow run and all its pending/running steps.

```
alcove workflows cancel <run-id>
```

### Description

Cancels a workflow run by setting its status to "cancelled" and cancelling all 
pending, running, or awaiting approval steps. Any active sessions associated 
with the workflow run will also be cancelled. Only workflow runs in `pending`, 
`running`, or `awaiting_approval` status can be cancelled.

On success, confirms that the workflow run has been cancelled.

### Examples

```bash
# Cancel a workflow run
alcove workflows cancel c2d3e4f5-a6b7-8901-bcde-f12345678901

# JSON output
alcove workflows cancel --output json c2d3e4f5-a6b7-8901-bcde-f12345678901
```
