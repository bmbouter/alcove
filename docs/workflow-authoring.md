# Workflow Authoring Guide

Alcove workflows chain multiple steps into automated pipelines. Each step is
either an AI coding agent (Claude Code in a Skiff pod) or a deterministic bridge
action (performed by Bridge with no LLM). Workflows can include bounded cycles
for review/revision loops.

## Step Types

Every workflow step has a `type` field:

- **`type: agent`** (default) — dispatches a Skiff pod running Claude Code.
  Use for coding, reviewing, debugging, and any task requiring LLM reasoning.
- **`type: bridge`** — Bridge performs a deterministic action inline.
  Use for infrastructure: creating PRs, polling CI, merging.

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
```

Bridge actions are faster, cheaper, and more reliable than asking an agent to
run shell commands for the same task.

## Bridge Actions Reference

### create-pr

Creates a GitHub/GitLab pull request from a branch.

| Input | Required | Description |
|-------|----------|-------------|
| `branch` | yes | Source branch name |
| `title` | yes | PR title |
| `base` | no | Target branch (default: `main`) |
| `body` | no | PR description |
| `draft` | no | Create as draft PR (default: `false`) |

**Outputs:** `pr_number`, `pr_url`

### await-ci

Polls CI status on a PR until all checks complete.

| Input | Required | Description |
|-------|----------|-------------|
| `pr` | yes | PR number |
| `timeout` | no | Max wait time in seconds (default: `900`) |

**Outputs:** `status` (`passed` or `failed`), `failure_logs`, `failed_checks`

The step succeeds if all checks pass; fails if any check fails.

### merge-pr

Merges a pull request.

| Input | Required | Description |
|-------|----------|-------------|
| `pr` | yes | PR number |
| `method` | no | `merge`, `squash`, or `rebase` (default: `merge`) |
| `delete_branch` | no | Delete source branch after merge (default: `true`) |

**Outputs:** `merge_sha`

## Dependencies with Depends Expressions

The `depends` field controls when a step runs using boolean expressions.
Each condition has the form `<step-id>.<Status>`.

**Status values:** `.Succeeded`, `.Failed`

**Operators:** `&&` (AND), `||` (OR), parentheses for grouping

```yaml
# Run after a single step succeeds
depends: "implement.Succeeded"

# Run after both reviews pass
depends: "code-review.Succeeded && security-review.Succeeded"

# Run when either review fails (trigger a revision)
depends: "code-review.Failed || security-review.Failed"

# Cycle entry point — first CI success OR after a fix
depends: "create-pr.Succeeded || ci-fix.Succeeded"

# Grouped expression
depends: "(code-review.Succeeded || revision.Succeeded) && security-review.Succeeded"
```

A step with no `depends` field is a root step and runs immediately.

**Backward compatibility:** The older `needs: [step1, step2]` list syntax is
still supported. It is equivalent to `depends: "step1.Succeeded && step2.Succeeded"`.

## Bounded Cycles (Review Loops)

Unlike a strict DAG, Alcove workflows allow steps to depend on later steps,
forming cycles. This enables natural review/revision patterns:

```yaml
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
```

The flow: `code-review` runs after CI passes. If it fails, `revision` runs.
After `revision` succeeds, `code-review` runs again (because of the `||`).
This cycle repeats until review passes or `max_iterations` is exhausted.

**How `max_iterations` works:**

- Default is `1` (step runs at most once -- backward compatible, no cycles)
- Each execution increments the step's visit count
- When `visit_count >= max_iterations`, the step status becomes
  `max_iterations_exceeded`
- Bridge escalates by adding a `needs-human-review` label and commenting on the PR

Set `max_iterations > 1` on every step involved in a cycle.

## Inputs and Outputs

### Template Variables

Step inputs support Go template variables for referencing data from triggers
and other steps:

```yaml
inputs:
  branch: "issue-{{trigger.issue_number}}-fix"
  pr: "{{steps.create-pr.outputs.pr_number}}"
  ci_logs: "{{steps.await-ci.outputs.failure_logs}}"
  feedback: "{{steps.code-review.outputs.comments}}"
```

| Variable | Description |
|----------|-------------|
| `{{trigger.issue_number}}` | Issue/PR number from the triggering event |
| `{{steps.<id>.inputs.<key>}}` | Input value from another step |
| `{{steps.<id>.outputs.<key>}}` | Output value from another step |

### Agent Step Outputs

Agent steps produce outputs by printing structured JSON. For example, a
reviewer agent outputs:

```
{"approved": false, "comments": "Missing error handling in handler.go:42"}
```

Other steps reference this as `{{steps.code-review.outputs.comments}}`.

### Bridge Action Outputs

Bridge actions produce outputs automatically (see the reference above for
each action's output fields).

## Step Credentials

Steps can declare credentials that override or augment the referenced agent's
credentials. This is useful when different steps need different API keys or when
a step needs additional secrets not defined in the agent.

```yaml
workflow:
  steps:
    - id: analyze
      type: agent
      agent: Log Analyzer
      credentials:
        SPLUNK_TOKEN: splunk-prod        # Override agent's default credential
        CUSTOM_WEBHOOK: slack-webhook    # Additional credential for this step
```

**Merge behavior:** Step credentials merge with agent credentials. Step values
override agent values for the same environment variable name. Agent credentials
not overridden are preserved.

```
Agent credentials:  {GITHUB_TOKEN: github, SPLUNK_TOKEN: splunk-staging}
Step credentials:   {SPLUNK_TOKEN: splunk-prod, CUSTOM_KEY: my-secret}
Result:             {GITHUB_TOKEN: github, SPLUNK_TOKEN: splunk-prod, CUSTOM_KEY: my-secret}
```

The `credentials` field uses the same format as agent-level credentials: keys
are environment variable names, values are credential provider names from the
credential store. See [Configuration Reference](configuration.md#credentials)
for details on creating and managing credentials.

## Triggers

Workflows run in response to events or on a schedule.

### GitHub Event Triggers

```yaml
trigger:
  github:
    events: [issues]
    actions: [labeled]
    repos: [org/repo]
    labels: [ready-for-dev]
    users: [bmbouter]
    delivery_mode: polling
```

Supported events: `issues`, `issue_comment`, `pull_request`, `push`.
Use `labels` and `users` fields for safety filtering.

### Schedule Triggers

```yaml
schedule: "0 2 * * *"
```

Cron syntax. See `docs/configuration.md` for the full trigger and schedule
reference.

## Complete Example: Full SDLC Pipeline

This workflow implements a complete develop-review-merge pipeline triggered
when an issue is labeled `ready-for-dev`.

```yaml
name: Feature Pipeline
trigger:
  github:
    events: [issues]
    actions: [labeled]
    repos: [org/myproject]
    labels: [ready-for-dev]

workflow:
  steps:
    # 1. Dev agent implements the feature
    - id: implement
      type: agent
      agent: dev
      inputs:
        branch: "issue-{{trigger.issue_number}}-fix"

    # 2. Bridge creates the PR (no LLM needed)
    - id: create-pr
      type: bridge
      action: create-pr
      depends: "implement.Succeeded"
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        title: "Fix #{{trigger.issue_number}}"
        base: main

    # 3. Bridge polls CI status
    - id: await-ci
      type: bridge
      action: await-ci
      depends: "create-pr.Succeeded || ci-fix.Succeeded"
      max_iterations: 4
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    # 4. If CI fails, dev agent fixes it (up to 3 attempts)
    - id: ci-fix
      type: agent
      agent: dev
      depends: "await-ci.Failed"
      max_iterations: 3
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        ci_logs: "{{steps.await-ci.outputs.failure_logs}}"

    # 5. Code review runs after CI passes (parallel with security review)
    - id: code-review
      type: agent
      agent: reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    # 6. Security review runs in parallel with code review
    - id: security-review
      type: agent
      agent: security-reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    # 7. If either review fails, dev agent revises
    - id: revision
      type: agent
      agent: dev
      depends: "code-review.Failed || security-review.Failed"
      max_iterations: 3
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        code_feedback: "{{steps.code-review.outputs.comments}}"
        security_feedback: "{{steps.security-review.outputs.comments}}"

    # 8. Bridge merges when both reviews pass
    - id: merge
      type: bridge
      action: merge-pr
      depends: "code-review.Succeeded && security-review.Succeeded"
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"
```

**Cycles in this workflow:**

1. `await-ci -> ci-fix -> await-ci` -- CI retry loop
2. `code-review/security-review -> revision -> code-review/security-review` -- review loop

Each cycle is bounded by `max_iterations`. If a step exhausts its iterations,
the workflow stops and Bridge escalates for human review.

## Writing Minimal Agent Prompts

With bridge actions handling PRs, CI, and merging, agent prompts should focus
on the task alone:

- **Keep prompts under 100 words.** Shorter prompts produce better results and
  cost less.
- **Focus on what to do, not how to interact with infrastructure.** Bridge
  actions handle PR creation, CI monitoring, and merging.
- **`$BRANCH` is set by the workflow engine.** Agents push to it automatically.
- **Use structured JSON outputs** so downstream steps can consume the results.

**Example -- dev agent (~40 words):**

```
You are a developer. Implement the changes described in the issue context.
Push your work to the $BRANCH branch.

Follow existing code patterns. Include tests for new functionality.
Run `go build ./...` and `go vet ./...` before pushing.
```

**Example -- reviewer agent (~30 words):**

```
Review this PR for correctness, bugs, and adherence to project conventions.
Post your review via the GitHub API.

Output {"approved": true} or {"approved": false, "comments": "..."}.
```

## Tips

- **Start simple.** Begin with 2-3 steps (implement, create-pr, merge) and add
  review loops once the basic flow works.
- **Test manually.** Trigger workflows by labeling an issue and watch the
  workflow run detail in the dashboard to debug.
- **`max_iterations` defaults to 1.** This means no cycles by default --
  existing workflows are unaffected. Set it higher on any step involved in a
  cycle.
- **Bridge actions use real credentials.** They are more secure than having an
  agent call APIs because there is no prompt injection risk.
- **Check the dashboard.** The workflow run detail page shows each step's
  status, iteration count, inputs, and outputs.
