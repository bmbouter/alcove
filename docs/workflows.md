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

A workflow step can target a different repo than the agent's default using the `repo` field:

```yaml
- id: implement-plugin
  agent: autonomous-developer
  repo: pulp/pulp_python

- id: implement-core
  agent: autonomous-developer
  repo: pulp/pulpcore
  needs: [implement-plugin]
  inputs:
    context: "{{steps.implement-plugin.outputs.summary}}"
```

The same agent definition runs against different repositories. This enables workflows that coordinate changes across multiple related repositories.

### Requirements

1. **Credentials**: The workflow owner must have appropriate credentials configured for each target repository service (GitHub, GitLab, etc.)
2. **Agent Definition**: The agent must be defined and available to the workflow owner
3. **Security Profile**: The agent's security profile must allow operations on multiple repos if cross-repo access is needed

### How It Works

When a workflow step specifies a different `repo`:

1. **Credential Validation**: Bridge validates that credentials exist for the target repository's service
2. **Task Enhancement**: The task request is enhanced with:
   - Repository URL override
   - Service-specific credential mapping  
   - Cross-repo context injection into the agent prompt
3. **Agent Dispatch**: The agent runs in a Skiff container configured for the target repository
4. **Output Collection**: Outputs are collected and made available to subsequent steps

### Security Considerations

Cross-repo workflows require careful credential scoping:

- **Owner-Specific Credentials**: Preferred for security isolation between users
- **Fallback Credentials**: Global service credentials are used if no owner-specific credentials exist
- **Credential Validation**: Bridge validates access before dispatching each cross-repo step

### Example: Multi-Repository Feature Development

```yaml
name: Multi-Repo Feature Pipeline
workflow:
  - id: plan-changes
    agent: feature-planner
    trigger:
      github:
        events: [issues] 
        labels: [feature-request]
    outputs: [implementation_plan, affected_repos]

  - id: implement-backend
    agent: autonomous-developer  
    repo: company/backend-service
    needs: [plan-changes]
    inputs:
      plan: "{{steps.plan-changes.outputs.implementation_plan}}"
    outputs: [backend_pr_url]

  - id: implement-frontend
    agent: autonomous-developer
    repo: company/frontend-app  
    needs: [plan-changes]
    inputs:
      plan: "{{steps.plan-changes.outputs.implementation_plan}}"
    outputs: [frontend_pr_url]

  - id: update-docs
    agent: documentation-agent
    repo: company/technical-docs
    needs: [implement-backend, implement-frontend]
    condition: "steps.implement-backend.outcome == 'completed' && steps.implement-frontend.outcome == 'completed'"
    inputs:
      backend_pr: "{{steps.implement-backend.outputs.backend_pr_url}}"
      frontend_pr: "{{steps.implement-frontend.outputs.frontend_pr_url}}"
```

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
