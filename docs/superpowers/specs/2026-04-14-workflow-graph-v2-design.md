# Workflow Graph v2 — Bridge Actions & Minimal Prompts

## Problem

Production agent prompts are ~75% infrastructure boilerplate (PR creation, CI
polling, retry logic, shell variable warnings, API curl commands) and only ~25%
actual task instructions. This makes prompts fragile, error-prone, and expensive
(large prompts degrade LLM performance). The root cause is that the workflow
engine treats every step as an agent dispatch — there is no concept of a
deterministic "bridge action" that the platform performs without an LLM.

Additionally, the current workflow engine is a strict DAG (directed acyclic
graph), which cannot represent the review/revision cycle that is fundamental
to software development. Users must work around this with separate workflow
runs or prompt-based retry loops.

## Solution

Evolve the workflow engine from a strict DAG to a **workflow graph with bounded
cycles**. Introduce two step types:

- **`type: agent`** — dispatches a Skiff pod running Claude Code (existing behavior)
- **`type: bridge`** — Bridge performs a deterministic action (new)

Bridge actions handle infrastructure concerns (PR creation, CI monitoring,
merging) so agent prompts can focus purely on the task. Bounded cycles enable
natural review/revision loops with `max_iterations` preventing infinite loops.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Graph model | Directed graph with bounded cycles | SDLC review/revision loops are inherently cyclic; strict DAG cannot represent them |
| Cycle bounding | `max_iterations` per step | Simple visit counter; when exhausted, step fails permanently |
| Step types | `agent` and `bridge` | Clean separation of LLM work from deterministic infrastructure |
| Dependency syntax | Argo-style `depends` expressions | `"A.Succeeded && B.Failed"` — widely known, expressive, readable |
| Bridge actions (v1) | `create-pr`, `await-ci`, `merge-pr` | Minimum set to eliminate PR/CI boilerplate from prompts |
| Security model | Bridge uses real tokens for bridge actions | More secure than LLM using tokens — deterministic, no prompt injection risk |
| Prompt philosophy | Agent prompts should be ~50 words | Task instructions only; environment/workflow concerns handled by Bridge |

## Research Basis

This design is informed by analysis of: Argo Workflows (DAG syntax, `depends`
expressions), Temporal (bounded loops via `continue-as-new`), AWS Step Functions
(state machine with native cycles), LangGraph (conditional edges with cycles for
AI agent workflows), GitHub Copilot agent (platform handles PRs/CI, agent just
codes), SWE-agent (tool design > prompt engineering), and the Workflow Patterns
academic taxonomy (WCP-21 Structured Loop).

Key industry finding: **the harness should enforce, not advise.** The most
reliable AI coding systems move infrastructure concerns out of the prompt and
into deterministic platform code. Anthropic's own research found they "spent
more time optimizing tools than overall prompts" for their SWE-bench agent.

## Workflow Graph YAML Schema

### Step Types

```yaml
workflow:
  steps:
    # Agent step — dispatches a Skiff pod running Claude Code
    - id: implement
      type: agent
      agent: dev
      max_retries: 3          # Retries on session failure (crash, timeout)
      max_iterations: 1       # How many times this step can be visited (default 1)
      inputs:
        branch: "issue-{{trigger.issue_number}}-fix"

    # Bridge step — Bridge performs a deterministic action
    - id: create-pr
      type: bridge
      action: create-pr
      depends: "implement.Succeeded"
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        title: "Fix #{{trigger.issue_number}}"
        base: main
```

### Dependency Expressions

The `depends` field uses Argo-style expressions with status conditions:

```
depends: "A.Succeeded"                          # Simple dependency
depends: "A.Succeeded && B.Succeeded"           # Both must succeed
depends: "A.Failed || B.Failed"                 # Either fails
depends: "await-ci.Succeeded || revision.Succeeded"  # Cycle entry point
```

Supported status values: `.Succeeded`, `.Failed`

Supported operators: `&&` (AND), `||` (OR), parentheses for grouping

A step with no `depends` field is a root step and runs immediately.

### Bounded Cycles

Steps track a visit count. Each time a step executes, its visit count
increments. When `visit_count >= max_iterations`, the step fails permanently
with status `max_iterations_exceeded`. Bridge adds a `needs-human-review`
label and posts a comment on the PR.

```yaml
    - id: code-review
      type: agent
      agent: reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3    # Can be visited up to 3 times
```

Default `max_iterations` is 1 (no revisiting — backward compatible with
current DAG behavior).

### Template Variables

Steps can reference outputs from other steps and trigger context:

```
{{trigger.issue_number}}              # From the triggering event
{{steps.implement.inputs.branch}}     # Input value of another step
{{steps.create-pr.outputs.pr_number}} # Output value of another step
{{steps.await-ci.outputs.failure_logs}}
{{steps.code-review.outputs.comments}}
```

## Built-in Bridge Actions

### create-pr

Creates a GitHub/GitLab pull request from a branch.

**Inputs:**
- `branch` (required) — source branch name
- `base` (required) — target branch (e.g., `main`)
- `title` (required) — PR title
- `body` (optional) — PR description
- `draft` (optional, default false) — create as draft PR

**Outputs:**
- `pr_number` — the created PR number
- `pr_url` — the PR URL

**Implementation:** Bridge calls the GitHub/GitLab API using the team's SCM
credential. No LLM involved.

### await-ci

Polls CI status on a PR until all checks complete or timeout.

**Inputs:**
- `pr` (required) — PR number
- `timeout` (optional, default 900s) — max wait time

**Outputs:**
- `status` — `passed` or `failed`
- `failure_logs` — truncated logs from failed checks (if any)
- `failed_checks` — list of check names that failed

**Implementation:** Bridge polls the GitHub/GitLab checks API. On failure,
fetches job logs and truncates to a reasonable size for the next step's context.

### merge-pr

Merges a pull request.

**Inputs:**
- `pr` (required) — PR number
- `method` (optional, default `merge`) — `merge`, `squash`, or `rebase`
- `delete_branch` (optional, default true) — delete source branch after merge

**Outputs:**
- `merge_sha` — the merge commit SHA

**Implementation:** Bridge calls the GitHub/GitLab merge API. Fails if the PR
has merge conflicts or required checks haven't passed.

## Complete Example: SDLC Workflow with Review Loop

```yaml
name: Full SDLC Pipeline
trigger:
  event: issues
  action: labeled
  label: ready-for-dev

workflow:
  steps:
    - id: implement
      type: agent
      agent: dev
      max_retries: 3
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
      depends: "create-pr.Succeeded || ci-fix.Succeeded"
      max_iterations: 4
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    - id: ci-fix
      type: agent
      agent: dev
      depends: "await-ci.Failed"
      max_iterations: 3
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        ci_logs: "{{steps.await-ci.outputs.failure_logs}}"

    - id: code-review
      type: agent
      agent: reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    - id: security-review
      type: agent
      agent: security-reviewer
      depends: "await-ci.Succeeded || revision.Succeeded"
      max_iterations: 3
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"

    - id: revision
      type: agent
      agent: dev
      depends: "code-review.Failed || security-review.Failed"
      max_iterations: 3
      inputs:
        branch: "{{steps.implement.inputs.branch}}"
        code_feedback: "{{steps.code-review.outputs.comments}}"
        security_feedback: "{{steps.security-review.outputs.comments}}"

    - id: merge
      type: bridge
      action: merge-pr
      depends: "code-review.Succeeded && security-review.Succeeded"
      inputs:
        pr: "{{steps.create-pr.outputs.pr_number}}"
```

### Flow Description

1. **implement** — dev agent writes code, pushes to branch
2. **create-pr** — Bridge creates the PR (deterministic, no LLM)
3. **await-ci** — Bridge polls CI status (deterministic)
4. **ci-fix** — if CI fails, dev agent fixes code (up to 3 iterations)
5. **code-review** + **security-review** — run in parallel (both depend on CI passing)
6. **revision** — if either reviewer rejects, dev agent revises
7. Back to reviews (cycle, up to 3 iterations)
8. **merge** — Bridge merges when both reviewers approve

### Cycles in this Graph

1. `await-ci → ci-fix → await-ci` — CI retry loop (max 3 ci-fix, max 4 await-ci)
2. `code-review/security-review → revision → code-review/security-review` — review loop (max 3 each)

When any step hits `max_iterations`, it fails with `max_iterations_exceeded`.
The workflow stops and Bridge escalates (label + comment on PR).

## Minimal Agent Prompts

With bridge actions handling infrastructure, agent prompts become minimal:

### dev agent (implement)

```
You are a developer. Implement the changes described in the issue context.
Push your work to the $BRANCH branch.

Follow existing code patterns. Include tests for new functionality.
Run `go build ./...` and `go vet ./...` before pushing.
```

### dev agent (ci-fix)

```
CI failed on this PR. The failure logs are provided in your context.
Fix the failing tests or build errors and push to $BRANCH.

Run `go build ./...` and `go vet ./...` before pushing.
```

### dev agent (revision)

```
Reviewers requested changes on this PR. Their feedback is in your context.
Address each comment and push your changes to $BRANCH.

Run `go build ./...` and `go vet ./...` before pushing.
```

### reviewer agent

```
Review this PR for correctness, bugs, and adherence to project conventions.
Post your review via the GitHub API.

Output {"approved": true} or {"approved": false, "comments": "..."}.
```

### security-reviewer agent

```
Review this PR for security vulnerabilities: injection, auth bypass,
credential exposure, unsafe deserialization, and OWASP Top 10.
Post your review via the GitHub API.

Output {"approved": true} or {"approved": false, "comments": "..."}.
```

## Engine Changes

### Visit Counter

The `workflow_run_steps` table gains an `iteration` column (INTEGER DEFAULT 0).
Each time the engine dispatches a step, it increments the iteration. Before
dispatching, it checks `iteration < max_iterations`.

### Step Dispatch Logic

When a step completes (succeeded or failed), the engine:

1. Updates the step's status
2. Evaluates all steps whose `depends` expression references this step
3. For each step where the `depends` expression evaluates to true:
   a. Check `iteration < max_iterations`
   b. If within limit: dispatch the step (increment iteration)
   c. If at limit: mark step as `max_iterations_exceeded`, escalate

### Bridge Action Dispatch

Bridge actions execute inline (no Skiff pod):

1. Engine recognizes `type: bridge`
2. Calls the appropriate action handler with resolved inputs
3. Action handler performs the API call and returns outputs
4. Engine records outputs and updates step status

### Depends Expression Parser

Parse `depends` strings into an AST of AND/OR/status conditions:

```
"A.Succeeded && (B.Failed || C.Succeeded)"
```

Evaluates against the current step statuses in the workflow run. A step with
an unsatisfied `depends` expression remains pending.

### Cycle Detection (Not Prevention)

The engine does NOT prevent cycles in the graph definition. Instead, it
relies on `max_iterations` to bound execution. At workflow parse time,
validate that every step involved in a cycle has `max_iterations > 1`
(warn if a cycle exists but a step has the default `max_iterations: 1`,
as it would immediately fail on the second visit).

## Migration

### Existing Workflows

Existing workflow definitions continue to work unchanged:
- Steps without `type` default to `type: agent`
- Steps without `max_iterations` default to 1 (no revisiting)
- The `depends` syntax already supports `needs` lists — add support for
  the expression syntax alongside the existing list syntax

### Existing Agent Definitions

Existing agent prompts continue to work. Prompt simplification is a separate
effort — agents with long prompts still function; they're just unnecessarily
verbose.

## Components Affected

- **internal/bridge/workflow_engine.go** — cycle support, visit counters,
  bridge action dispatch, enhanced depends parsing
- **internal/bridge/workflow.go** — YAML schema changes, validation
- **internal/bridge/bridge_actions.go** — new file for create-pr, await-ci,
  merge-pr implementations
- **internal/bridge/api.go** — expose bridge action metadata via API
- **web/js/app.js** — workflow visualization updates for bridge steps and cycles
- **docs/** — workflow authoring guide, architecture updates
- **.alcove/agents/** — simplified agent prompts
- **.alcove/workflows/** — updated workflow definitions using bridge actions

## Components NOT Affected

- **Gate** — no changes; bridge actions use real tokens directly, not Gate
- **Skiff** — no changes; agent steps dispatch Skiff pods as before
- **Hail (NATS)** — no changes
- **CLI** — no changes needed for v1

## Not in v1

- Conditional routing based on output content (beyond Succeeded/Failed)
- Matrix/fan-out strategies
- `finally` blocks (guaranteed cleanup)
- Approval gates (human-in-the-loop)
- Saga/compensation patterns
- Additional bridge actions (add-label, post-comment, send-notification)
- Workflow graph visualization of cycles in the dashboard

## Implementation Phases

### Phase 1: Engine + Bridge Actions

One tightly coordinated implementation covering:

1. Workflow engine: cycles, visit counters, `max_iterations`, expression parser
2. Bridge action framework: `type: bridge` dispatch, action registry
3. Built-in actions: `create-pr`, `await-ci`, `merge-pr`
4. YAML schema: `type` field, enhanced `depends` expressions
5. Database: `iteration` column on `workflow_run_steps`

### Phase 2: Prompts, Frontend, Tests, Docs (Parallel)

Five parallel agents:

1. **Prompt agent**: Rewrite production agent definitions to be minimal
2. **Frontend agent**: Update workflow UI to show bridge steps distinctly
3. **Test agent**: Functional tests for bridge actions and cycle execution
4. **Docs agent**: Architecture docs, workflow authoring guide
5. **Migration agent**: Update existing workflow definitions to use bridge actions
