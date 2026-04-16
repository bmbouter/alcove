# Compiled Agents

Compiled agents are pre-built binaries that run inside Skiff containers instead
of Claude Code. They receive the same environment variables and Gate proxy
access as Claude Code sessions, letting you write agents in any language.

## How Gate Proxying Works

Your compiled agent never sees real credentials. Here is the flow:

1. Bridge injects dummy tokens and proxy URLs into the Skiff container as
   environment variables.
2. Your agent makes API calls to the proxy URL using the dummy token.
3. Gate intercepts the request, validates the operation against the security
   profile scope, and swaps the dummy token for the real credential.
4. Gate forwards the request to the real API and returns the response.
5. Your agent receives the response without ever handling real secrets.

This applies to the LLM API, GitHub, GitLab, and Jira. For services that Gate
does not proxy (e.g., Splunk), use the `credentials` field to inject real
tokens directly (see [Direct Credential Injection](#direct-credential-injection)).

## Environment Variable Reference

Every variable listed below is available inside the Skiff container. Compiled
agents should read these at startup.

### Task Execution

| Variable | Always Set | Description |
|----------|-----------|-------------|
| `TASK_ID` | Yes | Task UUID |
| `SESSION_ID` | Yes | Session UUID |
| `PROMPT` | Yes | Agent prompt or task description |
| `TASK_TIMEOUT` | Yes | Timeout in seconds (default `3600`) |
| `TASK_BUDGET` | If configured | Max spend in USD |
| `BRANCH` | If set in workflow | Git branch name |

### LLM Provider

| Variable | Always Set | Description |
|----------|-----------|-------------|
| `ANTHROPIC_BASE_URL` | Yes | Gate proxy URL for LLM API calls |
| `ANTHROPIC_API_KEY` | Yes | Placeholder token -- Gate injects the real key |
| `CLAUDE_MODEL` | If configured | Model name override |

### Git

| Variable | Always Set | Description |
|----------|-----------|-------------|
| `REPO` | If configured | Repository URL to clone |
| `GIT_AUTHOR_NAME` | Yes | `Alcove` |
| `GIT_AUTHOR_EMAIL` | Yes | `alcove@localhost` |
| `GIT_COMMITTER_NAME` | Yes | `Alcove` |
| `GIT_COMMITTER_EMAIL` | Yes | `alcove@localhost` |

### GitHub (when scope includes `github`)

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | Dummy token -- routed through Gate |
| `GH_TOKEN` | Alias for `GITHUB_TOKEN` |
| `GITHUB_API_URL` | `http://gate-{id}:8443/github` |

### GitLab (when scope includes `gitlab`)

| Variable | Description |
|----------|-------------|
| `GITLAB_TOKEN` | Dummy token -- routed through Gate |
| `GITLAB_API_URL` | `http://gate-{id}:8443/gitlab/api/v4` |

### Jira (when scope includes `jira`)

| Variable | Description |
|----------|-------------|
| `JIRA_TOKEN` | Dummy token -- routed through Gate |
| `JIRA_API_URL` | `http://gate-{id}:8443/jira` |

### Infrastructure

| Variable | Description |
|----------|-------------|
| `HAIL_URL` | NATS URL for status updates |
| `LEDGER_URL` | Bridge URL for transcript storage |
| `SESSION_TOKEN` | Bearer token for Bridge auth |

## Task Definition for Compiled Agents

Define a compiled agent in a `.alcove/tasks/*.yml` file:

```yaml
name: My Agent
executable:
  url: https://github.com/org/repo/releases/download/v1.0/my-agent
  args: ["--flag", "value"]
  env:
    CUSTOM_VAR: "value"
timeout: 1800
profiles:
  - my-security-profile
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `executable.url` | string | yes | Download URL for the binary |
| `executable.args` | string[] | no | Command-line arguments |
| `executable.env` | map | no | Additional environment variables |
| `timeout` | int | no | Timeout in seconds (default `3600`) |
| `profiles` | string[] | no | Security profile names for scope enforcement |
| `credentials` | map | no | Direct credential injection (see below) |

An agent definition must have either `prompt` (Claude Code) or
`executable.url` (compiled agent), but not both.

### Direct Credential Injection

The `credentials` field injects real tokens from the credential store directly
into the Skiff container's environment. Use this for services that Gate does
not proxy:

```yaml
name: Splunk Log Analyzer
executable:
  url: https://github.com/org/repo/releases/download/v1/agent-splunk
credentials:
  SPLUNK_TOKEN: splunk
  SPLUNK_URL: splunk-url
```

The key is the environment variable name; the value is the credential provider
name in the credential store. Bridge resolves each provider name and injects
the real credential at dispatch time.

For Gate-proxied services (GitHub, GitLab, Jira), prefer using the `profiles`
field and the service environment variables instead. Gate provides
operation-level scope enforcement that direct injection does not.

### Direct Outbound Network Access

If your compiled agent needs to make direct API calls to services not proxied
by Gate, add `direct_outbound: true` to the agent definition:

```yaml
name: My Direct Agent
executable:
  url: https://example.com/agent-binary
direct_outbound: true
credentials:
  API_KEY: my-api-secret
```

This gives the Skiff container direct internet access on all runtimes (Podman,
Docker, and Kubernetes). The agent can make HTTP/HTTPS calls without routing
through Gate. The Gate sidecar still runs for LLM and SCM proxy if needed.

On Kubernetes, direct outbound adds an `alcove.dev/direct-outbound: "true"` pod
label and skips `HTTP_PROXY`/`HTTPS_PROXY` injection. The cluster must have the
`alcove-allow-direct-outbound` NetworkPolicy deployed to permit egress.

## Building a Compiled Agent

Follow these steps when writing a compiled agent:

1. **Use `ANTHROPIC_BASE_URL` for LLM calls** -- never hard-code the real API
   URL. Gate handles authentication and provider translation.
2. **Use `{SERVICE}_API_URL` for service calls** -- `GITHUB_API_URL`,
   `GITLAB_API_URL`, `JIRA_API_URL`.
3. **Use `{SERVICE}_TOKEN` for auth headers** -- these are dummy tokens. Gate
   swaps them for real credentials.
4. **Read `PROMPT`** for the task description or instructions.
5. **Write structured JSON to stdout** so downstream workflow steps can
   consume your output.
6. **Exit 0 on success, non-zero on failure.** Skiff reports the exit code
   back to Bridge.

## Example: Making API Calls Through Gate

### LLM call

```bash
curl -X POST "$ANTHROPIC_BASE_URL/v1/messages" \
  -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### GitHub call

```bash
curl -H "Authorization: Bearer $GITHUB_TOKEN" \
  "$GITHUB_API_URL/repos/owner/repo/issues"
```

### Jira call

```bash
curl -H "Authorization: Bearer $JIRA_TOKEN" \
  "$JIRA_API_URL/rest/api/2/search?jql=project=PROJ"
```

### Splunk call (direct credential injection)

```bash
# Requires credentials: { SPLUNK_TOKEN: splunk, SPLUNK_URL: splunk-url }
curl -X POST "$SPLUNK_URL/services/search/jobs" \
  -H "Authorization: Bearer $SPLUNK_TOKEN" \
  -d 'search=search index=main | head 10'
```

## Security Profiles for Compiled Agents

Security profiles control which operations your agent can perform through Gate.
Define profiles in `.alcove/security-profiles/*.yml`:

```yaml
name: my-agent-profile
tools:
  github:
    rules:
      - repos: ["org/repo"]
        operations: [read, read_issues, create_comment]
  jira:
    rules:
      - repos: ["PROJ"]
        operations: [read_issues, search_issues, add_comment]
```

Reference the profile in your agent definition:

```yaml
name: My Agent
executable:
  url: https://example.com/my-agent
profiles:
  - my-agent-profile
```

Gate enforces these rules at proxy time. Requests outside the allowed scope
are rejected with a 403 status. See
[Gate SCM Authorization](design/gate-scm-authorization.md) for the full
operation taxonomy.
