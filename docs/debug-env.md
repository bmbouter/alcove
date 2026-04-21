# Debug Environment Inspector

The `debug-env` utility is built into every Skiff container and prints all
environment variables with categorization, dummy token detection, and
secret masking.

## Usage

```bash
debug-env                  # Human-readable categorized output
debug-env --json           # Structured JSON output
debug-env --show-secrets   # Reveal masked sensitive values
debug-env --version        # Print version
```

## Running as an Agent

Create an agent definition in your repo (`.alcove/agents/debug-env.yml`)
with the same `credentials:` block as the agent you're debugging:

```yaml
name: Debug Environment Inspector
description: Prints all Skiff environment variables with dummy token detection
executable:
  url: file:///usr/local/bin/debug-env
timeout: 60
credentials:
  SPLUNK_TOKEN: splunk
  JIRA_TOKEN: jira
  MY_API_KEY: my-secret
```

The `credentials:` block is the key — copy it from whatever agent isn't
working. The debug-env binary prints exactly what the Skiff sees, showing
which values are real, dummy, or missing.

This shows up on the Schedules page with a "Run Now" button. Runs in
~5 seconds.

## Categories

| Category | Variables | Notes |
|----------|-----------|-------|
| Infrastructure | TASK_ID, SESSION_ID, HAIL_URL, etc. | Always set, safe values |
| LLM Provider | ANTHROPIC_BASE_URL, ANTHROPIC_API_KEY | API_KEY is a dummy placeholder |
| SCM Tokens | GITHUB_TOKEN, GITLAB_TOKEN, JIRA_TOKEN | All dummy — Gate swaps for real |
| SCM Gateway URLs | GITHUB_API_URL, GITLAB_API_URL | Gate proxy endpoints |
| Network Proxy | HTTP_PROXY, HTTPS_PROXY, NO_PROXY | Gate proxy settings |
| Plugins & Catalog | ALCOVE_PLUGINS, ALCOVE_SKILL_REPOS | Plugin/skill configuration |
| Git Config | GIT_AUTHOR_NAME, REPO, etc. | Git identity and repo settings |
| Generic Secrets | User-defined via credentials: map | Real values — masked by default |
| Runtime | HOME, PATH, USER, etc. | System environment |

## Annotations

- `[DUMMY]` — Fake token that Gate swaps for a real credential at proxy time
- `[MASKED]` — Real sensitive value, hidden by default (use `--show-secrets`)
- `[GATE-PROXY]` — URL pointing to the Gate sidecar proxy

## Dummy Token Patterns

- `alcove-session-*` prefix — dummy SCM tokens
- `sk-placeholder-routed-through-gate` — dummy LLM API key
- `http://gate-*:8443/*` — Gate proxy URLs

## Installation Status

When running inside a Skiff container (detected via `TASK_ID`), debug-env
reports what plugins, skill repos, MCP servers, and credentials were
requested versus what is actually installed on disk.

### Sections

| Section | Source | Checks |
|---------|--------|--------|
| Plugins | `ALCOVE_PLUGINS` env var (JSON) | `/tmp/alcove-plugins/{name}` exists |
| Skill Repos | `ALCOVE_SKILL_REPOS` env var (JSON) | `/tmp/alcove-skills/{name}` exists and is a directory |
| MCP Servers | `ALCOVE_MCP_CONFIG` env var + `~/.claude.json` | Listed as configured |
| Credentials | All env vars | Classified as dummy (gate-proxied) or real |

### Status Values

- `[OK]` / `installed` — found on disk at expected path
- `[MISS]` / `missing` — requested but not found on disk
- `requested` — marketplace/official plugin, cannot verify filesystem install
- `configured` — MCP server declared in config
- `[DUMMY]` / `dummy` — credential is a placeholder swapped by Gate at proxy time
- `[REAL]` / `sensitive` — credential has a real value

### Example Output

```
=== Installation Status ===

  Plugins (requested: 2):
    [OK]   code-review               (claude-plugins-official)
    [MISS] custom-lint                (https://github.com/org/lint)

  Skill Repos (requested: 1):
    [OK]   my-skills                  (lola module)

  MCP Servers (configured: 1):
    [OK]   github

  Credentials (2):
    ANTHROPIC_API_KEY              [DUMMY] gate-proxied
    GITHUB_TOKEN                   [DUMMY] gate-proxied
```

In JSON mode (`--json`), the report appears under the `"installation"` key.

## Rebuilding

If you modify `cmd/debug-env/main.go`, rebuild the Skiff image:

```bash
make build-skiff    # Rebuilds only the Skiff base image (~2-3 min)
```

The next dispatched session will use the updated binary.
