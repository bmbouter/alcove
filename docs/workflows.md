# Workflow Orchestration

Workflows coordinate multiple agents across repos, systems, and time.
Each step is an isolated agent session — Bridge evaluates the DAG,
dispatches steps when dependencies are met, and passes outputs between them.

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
    agent: autonomous-developer
    repo: pulp/pulp_python
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    outputs: [pr_url, summary]

  - id: deploy-staging
    agent: deploy-agent
    needs: [implement]
    condition: "steps.implement.outcome == 'completed'"
    inputs:
      context: "{{steps.implement.outputs.summary}}"
      pr_url: "{{steps.implement.outputs.pr_url}}"

  - id: verify
    agent: smoke-test
    needs: [deploy-staging]

  - id: promote-prod
    agent: deploy-agent
    needs: [verify]
    approval: required
    inputs:
      environment: production
```

### Step Fields

| Field | Required | Description |
|-------|----------|-------------|
| `id` | Yes | Unique step identifier |
| `agent` | Yes | Name of the agent definition to run |
| `repo` | No | Override the agent's default repo |
| `needs` | No | List of step IDs that must complete first |
| `condition` | No | Expression that must evaluate to true before dispatch |
| `inputs` | No | Key-value map injected into the agent's prompt |
| `outputs` | No | List of output keys the step produces |
| `approval` | No | Set to `"required"` for human approval before dispatch |
| `route_field` | No | Output field name for field-based routing decisions |
| `route_map` | No | Value-to-step mapping for field-based routing |
| `trigger` | No | Event trigger (only on the first step) |

## Output Contract

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

## Input Injection

Inputs from previous steps are resolved using template syntax and
prepended to the agent's prompt as a "Workflow Context" section:

```yaml
inputs:
  context: "{{steps.implement.outputs.summary}}"
  pr_url: "{{steps.implement.outputs.pr_url}}"
  environment: staging  # static values work too
```

## Condition Evaluation

Conditions gate whether a step runs. Supports:

- `steps.X.outcome` — step outcome (completed, failed, skipped)
- `steps.X.outputs.Y` — output value from a step
- Operators: `==`, `!=`, `>`, `<`, `>=`, `<=`
- Boolean: `&&`, `||`
- Parentheses for grouping

```yaml
condition: "steps.test.outcome == 'completed' && steps.test.outputs.coverage > 80"
```

If the condition evaluates to false, the step is marked as `skipped`.

## Field-Based Routing

For simpler routing decisions, workflows support field-based routing using
`route_field` and `route_map`. This approach works well when routing decisions
come from a single output field from the completed step.

```yaml
- id: triage
  agent: triage-agent
  route_field: is_clear
  route_map:
    "true": planning      # → planning step
    "false": manual-review # → manual-review step
  outputs: [triage_result, is_clear]

- id: planning
  agent: planning-agent
  needs: [triage]

- id: manual-review
  agent: manual-review-agent
  needs: [triage]
```

When the `triage` step completes, the workflow engine:

1. Reads the value of `outputs.is_clear` from the triage step
2. Looks up the value in the `route_map` 
3. Automatically dispatches the corresponding step if its dependencies are satisfied

**Route Field Requirements:**
- Must be a valid identifier (alphanumeric + underscore)
- Both `route_field` and `route_map` must be specified together
- Route map values must reference valid step IDs in the workflow

Field-based routing is recommended for single-field decisions. Use `condition` 
expressions for more complex cross-step logic.

## Approval Gates

Steps with `approval: required` pause the workflow and wait for human action:

```yaml
- id: promote-prod
  agent: deploy-agent
  needs: [verify]
  approval: required
```

Approve or reject via the API:

```bash
# Approve
curl -X POST /api/v1/workflow-runs/{run_id}/approve/{step_id}

# Reject
curl -X POST /api/v1/workflow-runs/{run_id}/reject/{step_id}
```

The dashboard Workflows page shows pending approvals with approve/reject buttons.

## Cross-Repo Steps

A workflow step can target a different repo than the agent's default:

```yaml
- id: implement-plugin
  agent: autonomous-developer
  repo: pulp/pulp_python

- id: implement-core
  agent: autonomous-developer
  repo: pulp/pulpcore
  needs: [implement-plugin]
```

The same agent definition runs against different repos. Credentials for
each repo must be configured in the user's credential store.

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/workflows` | List workflow definitions |
| GET | `/api/v1/workflow-runs` | List workflow runs (supports `?status=` filter) |
| GET | `/api/v1/workflow-runs/{id}` | Get run detail with all steps and outputs |
| POST | `/api/v1/workflow-runs` | Manually trigger a workflow run |
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

## Examples

### Simple Two-Step: Implement + Review

```yaml
name: Implement and Review
workflow:
  - id: implement
    agent: autonomous-developer
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    outputs: [pr_url]

  - id: review
    agent: pr-reviewer
    needs: [implement]
    inputs:
      pr_url: "{{steps.implement.outputs.pr_url}}"
```

### Full SDLC Pipeline

```yaml
name: Full SDLC Pipeline
workflow:
  - id: implement
    agent: autonomous-developer
    trigger:
      github:
        events: [issues]
        labels: [ready-for-dev]
    outputs: [pr_url, summary]

  - id: review
    agent: pr-reviewer
    needs: [implement]
    outputs: [approved]

  - id: deploy-staging
    agent: deploy-agent
    needs: [review]
    condition: "steps.review.outcome == 'completed'"
    inputs:
      environment: staging

  - id: verify-staging
    agent: smoke-test
    needs: [deploy-staging]

  - id: promote-prod
    agent: deploy-agent
    needs: [verify-staging]
    approval: required
    inputs:
      environment: production

  - id: monitor
    agent: monitor-agent
    needs: [promote-prod]
```

### Field-Based Routing for Triage

```yaml
name: Issue Triage and Routing
workflow:
  - id: triage
    agent: triage-agent
    trigger:
      github:
        events: [issues]
        labels: [needs-triage]
    route_field: triage_decision
    route_map:
      clear: implement          # Clear requirements → implement
      unclear: clarification    # Unclear → request clarification
      duplicate: close-duplicate # Duplicate → close as duplicate
    outputs: [triage_decision, analysis, confidence]

  - id: implement
    agent: autonomous-developer
    needs: [triage]
    inputs:
      requirements: "{{steps.triage.outputs.analysis}}"

  - id: clarification
    agent: clarification-agent
    needs: [triage]
    inputs:
      analysis: "{{steps.triage.outputs.analysis}}"
      confidence: "{{steps.triage.outputs.confidence}}"

  - id: close-duplicate
    agent: close-issue-agent
    needs: [triage]
    inputs:
      reason: "Duplicate issue"
      analysis: "{{steps.triage.outputs.analysis}}"
```
