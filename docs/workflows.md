# Workflow Orchestration

Workflows coordinate multiple steps across repos, systems, and time.
Each step is either an AI coding agent (Claude Code in a Skiff pod) or a
deterministic bridge action (performed by Bridge with no LLM). Bridge
evaluates the dependency graph, dispatches steps when dependencies are met,
and passes outputs between them. Workflows support bounded cycles for
review/revision loops.

## Defining Workflows

Workflow YAML files live in `.alcove/workflows/` alongside agent definitions:

```
.alcove/
  tasks/              # agent definitions
  workflows/          # workflow definitions
  security-profiles/  # security profiles
```

### Syntax

```yaml
name: Feature Delivery Pipeline
workflow:
  - id: implement
    type: agent
    agent: dev
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    inputs:
      branch: "issue-{{trigger.issue_number}}-fix"

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
    depends: "create-pr.Succeeded"
    inputs:
      pr: "{{steps.create-pr.outputs.pr_number}}"

  - id: review
    type: agent
    agent: reviewer
    depends: "await-ci.Succeeded"
    inputs:
      pr: "{{steps.create-pr.outputs.pr_number}}"

  - id: merge
    type: bridge
    action: merge-pr
    depends: "review.Succeeded"
    inputs:
      pr: "{{steps.create-pr.outputs.pr_number}}"
```

### Step Types

Every workflow step has a `type` field:

- **`type: agent`** (default) -- dispatches a Skiff pod running Claude Code.
  Use for coding, reviewing, debugging, and any task requiring LLM reasoning.
- **`type: bridge`** -- Bridge performs a deterministic action inline.
  Use for infrastructure: creating PRs, polling CI, merging.

Bridge actions are faster, cheaper, and more reliable than asking an agent to
run shell commands for the same task.

### Step Fields

| Field | Required | Description |
|-------|----------|-------------|
| `id` | Yes | Unique step identifier |
| `type` | No | `agent` (default) or `bridge` |
| `agent` | Yes (agent steps) | Name of the agent definition to run |
| `action` | Yes (bridge steps) | Bridge action: `create-pr`, `await-ci`, or `merge-pr` |
| `repo` | No | Override the agent's default repo |
| `depends` | No | Boolean expression controlling when the step runs (see below) |
| `needs` | No | Legacy: list of step IDs that must complete first |
| `condition` | No | Legacy: expression evaluated before dispatch |
| `inputs` | No | Key-value map injected into the agent's prompt or bridge action |
| `outputs` | No | List of output keys the step produces (agent steps) |
| `approval` | No | Set to `"required"` for human approval before dispatch |
| `trigger` | No | Event trigger (only on the first step) |
| `max_iterations` | No | Max times this step can execute (default `1`; set higher for cycles) |
| `credentials` | No | Step-level credential overrides (see workflow-authoring.md) |
| `direct_outbound` | No | Enable direct internet access, bypassing Gate proxy |

## Bridge Actions Reference

Bridge actions run inside Bridge with no LLM. They use team credentials
from the credential store to call SCM APIs directly.

### create-pr

Creates a GitHub/GitLab pull request from a branch.

```yaml
- id: create-pr
  type: bridge
  action: create-pr
  depends: "implement.Succeeded"
  inputs:
    repo: "org/myproject"
    branch: "{{steps.implement.inputs.branch}}"
    title: "Fix #{{trigger.issue_number}}"
    base: main
    body: "Automated PR from workflow"
    draft: false
```

| Input | Required | Description |
|-------|----------|-------------|
| `repo` | Yes | Repository in `owner/repo` format |
| `branch` | Yes | Source branch name |
| `title` | Yes | PR title |
| `base` | Yes | Target branch name |
| `body` | No | PR description |
| `draft` | No | Create as draft PR (default: `false`) |

**Outputs:** `pr_number` (int), `pr_url` (string)

### await-ci

Polls CI status on a PR until all checks complete or the timeout expires.

```yaml
- id: await-ci
  type: bridge
  action: await-ci
  depends: "create-pr.Succeeded || ci-fix.Succeeded"
  max_iterations: 4
  inputs:
    repo: "org/myproject"
    pr: "{{steps.create-pr.outputs.pr_number}}"
    timeout: 900
```

| Input | Required | Description |
|-------|----------|-------------|
| `repo` | Yes | Repository in `owner/repo` format |
| `pr` | Yes | Pull request number |
| `timeout` | No | Max wait time in seconds (default: `900`) |

**Outputs:** `status` (`passed` or `failed`), `failure_logs` (string),
`failed_checks` (list of strings)

**Behavioral details:**

- Polls every 30 seconds.
- A check is considered passing if its conclusion is `success` or `skipped`.
- **No-CI heuristic:** If no check runs appear within 90 seconds of the first
  poll, the step treats CI as passed. This handles repos with no CI configured.
- When CI fails, Bridge fetches the last 3000 characters of each failed
  check's logs and includes them in `failure_logs`. Downstream agent steps
  can use this to diagnose and fix failures.
- The step itself always succeeds (status `completed`) as long as it gets a
  CI result. The CI outcome is in the `status` output field. Because the
  step always succeeds (even when CI fails), using `depends: "await-ci.Failed"`
  to trigger a CI-fix step will only fire on timeout, not on CI failure.
  Downstream steps that depend on `await-ci.Succeeded` should inspect
  `outputs.status` to determine whether CI actually passed.
- If the timeout expires before all checks complete, the step fails with an
  error.

### merge-pr

Merges a pull request.

```yaml
- id: merge
  type: bridge
  action: merge-pr
  depends: "code-review.Succeeded && security-review.Succeeded"
  inputs:
    repo: "org/myproject"
    pr: "{{steps.create-pr.outputs.pr_number}}"
    method: squash
    delete_branch: true
```

| Input | Required | Description |
|-------|----------|-------------|
| `repo` | Yes | Repository in `owner/repo` format |
| `pr` | Yes | Pull request number |
| `method` | No | `merge`, `squash`, or `rebase` (default: `merge`) |
| `delete_branch` | No | Delete source branch after merge (default: `true`) |

**Outputs:** `merge_sha` (string)

## Dependencies with Depends Expressions

The `depends` field controls when a step runs using boolean expressions.
Each condition has the form `<step-id>.<Status>`.

**Status values:** `.Succeeded` (step completed successfully), `.Failed`
(step failed)

**Operators:** `&&` (AND), `||` (OR), parentheses for grouping

An unresolved step (not yet finished, or status is `pending`/`running`)
evaluates to `false`. The step waits until the expression can be fully
evaluated and is `true`.

```yaml
# Run after a single step succeeds
depends: "implement.Succeeded"

# Run after both reviews pass
depends: "code-review.Succeeded && security-review.Succeeded"

# Run when either review fails (trigger a revision)
depends: "code-review.Failed || security-review.Failed"

# Cycle entry point -- first CI success OR after a fix
depends: "create-pr.Succeeded || ci-fix.Succeeded"

# Grouped expression
depends: "(code-review.Succeeded || revision.Succeeded) && security-review.Succeeded"
```

A step with no `depends` field and no `needs` field is a root step and runs
immediately when the workflow starts.

**Backward compatibility:** The older `needs: [step1, step2]` list syntax is
still supported. It is equivalent to
`depends: "step1.Succeeded && step2.Succeeded"`. If both `needs` and `depends`
are specified, `depends` takes precedence.

**Unreachable step detection:** When all steps referenced in a `depends`
expression have reached a terminal state (completed, failed, or skipped) but
the expression evaluates to `false`, Bridge marks the step as `skipped`. This
prevents workflows from hanging on steps that can never be triggered.

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

- Default is `1` (step runs at most once -- backward compatible, no cycles).
- Each execution increments the step's iteration counter.
- When the counter reaches `max_iterations`, the step is marked as `failed`
  with output `{"error": "max_iterations_exceeded"}`.
- Bridge then evaluates dependent steps normally. Steps that depend on the
  exhausted step via `.Failed` will fire; steps that depend via `.Succeeded`
  become unreachable and are marked `skipped`.
- If the exhausted step is the last active step in a cycle, the workflow
  completes with status `failed`.

Set `max_iterations > 1` on every step involved in a cycle. The workflow
validator warns when a cycle participant has `max_iterations` set to 1.

## Inputs and Outputs

### Template Variables

Step inputs support template variables for referencing data from triggers
and other steps:

```yaml
inputs:
  branch: "issue-{{trigger.issue_number}}-fix"
  pr: "{{steps.create-pr.outputs.pr_number}}"
  ci_logs: "{{steps.await-ci.outputs.failure_logs}}"
  feedback: "{{steps.code-review.outputs.comments}}"
  original_branch: "{{steps.implement.inputs.branch}}"
```

| Variable | Description |
|----------|-------------|
| `{{trigger.issue_number}}` | Issue/PR number from the triggering event |
| `{{steps.<id>.outputs.<key>}}` | Output value from another step |
| `{{steps.<id>.inputs.<key>}}` | Resolved input value from another step |

The `{{steps.<id>.inputs.<key>}}` pattern is useful when a later step needs
to reference the same computed input that was passed to an earlier step. For
example, a `create-pr` bridge step can reference the branch name that was
passed to the `implement` agent step. Bridge stores resolved inputs internally
so they are available for downstream template expansion.

Unresolved templates (referencing a step that has not yet run) are left as
literal strings in the input value.

### Agent Step Outputs

Agent steps produce outputs by printing structured JSON. For example, a
reviewer agent outputs:

```
{"approved": false, "comments": "Missing error handling in handler.go:42"}
```

Other steps reference this as `{{steps.code-review.outputs.comments}}`.

### Bridge Action Outputs

Bridge actions produce outputs automatically. See the Bridge Actions
Reference above for each action's output fields.

### Output Contract

Agents produce structured outputs by writing JSON to `/tmp/alcove-outputs.json`
before exiting:

```json
{
  "pr_url": "https://github.com/org/repo/pull/42",
  "summary": "Implemented feature X with tests",
  "commit_sha": "abc123"
}
```

Any language can write this file. Skiff-init reads it on completion and
stores the outputs in the workflow run state.

### Input Injection for Agent Steps

For agent steps, resolved inputs are prepended to the agent's prompt as a
"Workflow Context" section:

```
Workflow Context (from previous steps):
  branch: issue-42-fix
  ci_logs: ### lint
  ...

<original agent prompt follows>
```

Bridge steps receive resolved inputs as action parameters directly.

## Condition Evaluation (Legacy)

The `condition` field is the older mechanism for gating step execution.
New workflows should use `depends` instead.

Conditions support:

- `steps.X.outcome` -- step outcome (completed, failed, skipped)
- `steps.X.outputs.Y` -- output value from a step
- Operators: `==`, `!=`, `>`, `<`, `>=`, `<=`
- Boolean: `&&`, `||`
- Parentheses for grouping

```yaml
condition: "steps.test.outcome == 'completed' && steps.test.outputs.coverage > 80"
```

If the condition evaluates to false, the step is marked as `skipped`.

## Approval Gates

Steps with `approval: required` pause the workflow and wait for human action:

```yaml
- id: promote-prod
  type: agent
  agent: deploy-agent
  depends: "verify.Succeeded"
  approval: required
```

Approve or reject via the API:

```bash
# Approve
curl -X POST /api/v1/workflow-runs/{run_id}/approve/{step_id}

# Reject
curl -X POST /api/v1/workflow-runs/{run_id}/reject/{step_id}
```

When a step is approved, its `approval` requirement is cleared and the step
is dispatched normally. When rejected, the step is marked as `failed` and
the workflow run is marked as `failed`.

The dashboard Workflows page shows pending approvals with approve/reject buttons.

## Cross-Repo Steps

A workflow step can target a different repo than the agent's default:

```yaml
- id: implement-plugin
  type: agent
  agent: dev
  repo: pulp/pulp_python

- id: implement-core
  type: agent
  agent: dev
  repo: pulp/pulpcore
  depends: "implement-plugin.Succeeded"
```

The same agent definition runs against different repos. Credentials for
each repo must be configured in the team's credential store.

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/workflows` | List workflow definitions |
| GET | `/api/v1/workflow-runs` | List workflow runs (supports `?status=` filter) |
| GET | `/api/v1/workflow-runs/{id}` | Get run detail with all steps and outputs |
| POST | `/api/v1/workflow-runs/{id}/approve/{step_id}` | Approve a pending step |
| POST | `/api/v1/workflow-runs/{id}/reject/{step_id}` | Reject a pending step |

## Dashboard

The Workflows page (`#workflows`) shows:
- List of workflow runs with status, start time, current step
- Status filter (all, running, completed, failed, awaiting_approval)
- Click through to run detail showing steps with status indicators
- Inline approve/reject buttons for pending approval steps

## Syncing

Workflow YAML files are discovered and synced from agent repos alongside
agent definitions. The syncer looks for `*.yml` files in `.alcove/workflows/`.

Bridge validates agent references at sync time. If a workflow step references
an agent that is unknown (not in any catalog source) or disabled (not enabled
for the team), Bridge reports the error during sync.

## Examples

### Simple Three-Step: Implement, Create PR, Merge

```yaml
name: Implement and Merge
workflow:
  - id: implement
    type: agent
    agent: dev
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    inputs:
      branch: "issue-{{trigger.issue_number}}-fix"

  - id: create-pr
    type: bridge
    action: create-pr
    depends: "implement.Succeeded"
    inputs:
      repo: "org/myproject"
      branch: "{{steps.implement.inputs.branch}}"
      title: "Fix #{{trigger.issue_number}}"
      base: main

  - id: merge
    type: bridge
    action: merge-pr
    depends: "create-pr.Succeeded"
    inputs:
      repo: "org/myproject"
      pr: "{{steps.create-pr.outputs.pr_number}}"
```

### Full SDLC Pipeline with Review Loops

```yaml
name: Full SDLC Pipeline
trigger:
  github:
    events: [issues]
    actions: [labeled]
    repos: [org/myproject]
    labels: [ready-for-dev]

workflow:
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
      repo: "org/myproject"
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
      repo: "org/myproject"
      pr: "{{steps.create-pr.outputs.pr_number}}"

  # 4. If await-ci times out, dev agent investigates (up to 3 attempts)
  # Note: await-ci succeeds even when CI fails (the CI outcome is in
  # outputs.status). This step only runs if await-ci itself fails
  # (e.g., timeout). Downstream agent steps that run after CI success
  # should check outputs.status to handle CI failures.
  - id: ci-fix
    type: agent
    agent: dev
    depends: "await-ci.Failed"
    max_iterations: 3
    inputs:
      branch: "{{steps.implement.inputs.branch}}"
      ci_logs: "{{steps.await-ci.outputs.failure_logs}}"

  # 5. Code review after CI passes (or after revision succeeds)
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
      repo: "org/myproject"
      pr: "{{steps.create-pr.outputs.pr_number}}"
```

**Cycles in this workflow:**

1. `await-ci -> ci-fix -> await-ci` -- CI retry loop (fires only on
   await-ci timeout; see behavioral details above)
2. `code-review/security-review -> revision -> code-review/security-review` -- review loop

Each cycle is bounded by `max_iterations`. If a step exhausts its iterations,
the step fails with `{"error": "max_iterations_exceeded"}` and Bridge
evaluates dependent steps normally.
