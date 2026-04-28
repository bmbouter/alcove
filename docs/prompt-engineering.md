# Prompt Engineering

Alcove assembles the final prompt from multiple sources before passing it
to Claude Code. Understanding this pipeline helps you write effective agent
definitions and debug unexpected behavior.

## Prompt Assembly Order

The final prompt is built in this order (top to bottom):

| Position | Source | When Applied | Set By |
|----------|--------|--------------|--------|
| 1 (top) | Triple Team wrapper | `triple_team: true` | Dispatcher |
| 2 | Workflow context | Workflow step inputs | Workflow Engine |
| 3 | CI retry override | CI gate retry | CI Gate Monitor |
| 4 | **Your prompt** | Always | Agent definition or CLI |
| 5 | Event context | Event-triggered sessions | Poller |
| 6 (bottom) | CLAUDE.md | Repo contains CLAUDE.md | Skiff (at runtime) |

Agents read their task instructions first (positions 1–4), then receive
project context at the end (positions 5–6).

## Prompt Sources

### Your Prompt (Agent Definition)

The core prompt comes from the `prompt:` field in your agent YAML:

```yaml
name: Code Reviewer
prompt: |
  Review the PR for correctness and style.
  Post inline comments using the gh CLI.
```

For CLI-dispatched sessions, the prompt is the positional argument:

```bash
alcove run "Fix the bug in auth.go" --repo org/repo
```

### CLAUDE.md Injection

If the cloned repo contains a `CLAUDE.md` file, its contents are
**appended** to the end of the prompt. This provides project-specific
context (coding conventions, architecture notes, build commands) without
cluttering the task instructions.

For multi-repo clones, each repo's `CLAUDE.md` is appended with `---`
separators.

**Example:** With a repo that has `CLAUDE.md` containing Go conventions:

```
[Your prompt: "Fix the auth bug"]

---

# Project Instructions (from CLAUDE.md)
- Use Go 1.25
- Run tests with `make test`
- Follow standard Go project layout
```

### Triple Team Mode

Setting `triple_team: true` prepends a methodology wrapper that
instructs Claude Code to work in three phases using parallel sub-agents:

1. **Workers** — 3 specialists produce independent solutions
2. **Evaluators** — 3 reviewers critique and improve
3. **Integrators** — 3 agents synthesize the final result

```yaml
name: Complex Feature Builder
prompt: "Implement the caching layer"
triple_team: true
```

The wrapper text appears literally in the session transcript, so you
can see exactly what instructions the agent received.

CLI usage:

```bash
alcove run --triple-team "Implement the caching layer"
```

Dashboard: toggle the "Triple team" switch on the New Session form.

### Workflow Context

When an agent runs as part of a workflow, outputs from previous steps
are prepended as structured context:

```
Workflow Context (from previous steps):
  commit_sha: abc123
  pr_url: https://github.com/org/repo/pull/42
  ci_status: passed

[Your prompt]
```

See [Workflow Authoring](workflow-authoring.md) for details.

### Event Context

When an agent is triggered by a GitHub event (issue opened, PR created,
label added), enriched context is appended to the prompt with full
event details:

```
[Your prompt]

## Event Context

**Event**: issues / labeled
**Repository**: org/repo
**Label Added**: needs-planning

### Issue #42: Add caching layer
**State**: open
**Author**: @user
...
```

### CI Retry Override

When a CI gate detects failures on a PR created by an agent, it
dispatches a retry with modified instructions:

```
## CI Retry Context
**IMPORTANT**: You are fixing CI failures on an existing PR...
[failure details]

## OVERRIDE: CI Retry Mode
- Do NOT create a new branch or new PR
- Fix ONLY the CI failures described above

[Original prompt]
```

## Complete Example

A triple-team agent triggered by a GitHub event on a repo with
CLAUDE.md produces this prompt structure:

```
## Triple Team Mode                     ← triple_team wrapper
[3-phase parallel agent instructions]
---
Review the PR for security issues.      ← your prompt
Post findings as inline comments.

## Event Context                        ← event trigger context
**Event**: pull_request / opened
**Repository**: org/repo
### PR #42: Add authentication
[PR details, diff, CI status]

[event: {"GITHUB_EVENT":"pull_request",...}]

---

# Project Instructions (from CLAUDE.md) ← CLAUDE.md (appended)
- Use Go 1.25
- Security-sensitive code requires review
```

## Tips

- Keep your prompt focused on **what** to do. Let CLAUDE.md handle
  project conventions and **how** to build/test.
- Use `triple_team: true` for complex tasks where multiple perspectives
  improve quality. It spawns 9 sub-agents, so allow a higher budget
  and timeout.
- Event context is automatic — your prompt doesn't need to reference
  the event details; they're appended for you.
- The prompt is visible in the session transcript on the dashboard,
  so you can always verify what the agent actually received.
