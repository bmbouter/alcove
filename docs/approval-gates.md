# Workflow Approval Gates

This document describes the approval gates feature for Alcove workflow orchestration.

## Overview

Workflow steps can require human approval before executing. When a step has `approval: required`, the workflow will pause and wait for manual approval before proceeding.

## YAML Configuration

```yaml
- id: promote-prod
  agent: deploy-agent
  needs: [verify-staging]
  approval: required
  approval_timeout: 72h  # optional, defaults to 72h
```

### Fields

- `approval`: Set to `"required"` to require approval for this step
- `approval_timeout`: Optional timeout duration (e.g. "72h", "4h", "30m"). Defaults to 72 hours.

## Implementation

### Engine Behavior

1. When the engine reaches a step with `approval: required`:
   - Sets the step status to `awaiting_approval`
   - Sets the workflow run status to `awaiting_approval` if no other steps are running
   - Calculates approval deadline based on `approval_timeout` (default 72h)
   - Pauses execution until approval is granted

2. **Timeout Handling**: A background process runs every minute to check for expired approvals and marks them as cancelled.

### API Endpoints

- `GET /api/v1/workflow-runs` — list workflow runs with status
- `GET /api/v1/workflow-runs/{id}` — get workflow run details including pending approvals
- `POST /api/v1/workflow-runs/{id}/steps/{step_id}?action=approve` — approve a step
- `POST /api/v1/workflow-runs/{id}/steps/{step_id}?action=reject` — reject a step (fails the workflow)

### Dashboard UI

- **Workflows tab**: Shows workflow runs with approval status
- **Workflow detail page**: Displays step status with prominent approval cards
- **Approval actions**: Approve/reject buttons for steps awaiting approval
- **Deadline display**: Shows approval deadline and highlights expired approvals

## Database Schema

New fields added to `workflow_run_steps`:

```sql
ALTER TABLE workflow_run_steps ADD COLUMN approval_deadline TIMESTAMPTZ;
CREATE INDEX ON workflow_run_steps(approval_deadline) WHERE status = 'awaiting_approval';
```

## Status Values

- `awaiting_approval` — step is waiting for human approval
- `cancelled` — workflow cancelled due to approval timeout

## Security

- Only the workflow run owner can approve/reject steps
- API validates ownership before allowing approval actions
- Approval timeouts prevent workflows from hanging indefinitely

## Example Workflow

```yaml
name: Production Deployment Pipeline
workflow:
  - id: build
    agent: build-agent
    outputs: [artifact_url]

  - id: deploy-staging  
    agent: deploy-agent
    needs: [build]
    inputs:
      artifact: "{{steps.build.outputs.artifact_url}}"

  - id: integration-tests
    agent: test-agent
    needs: [deploy-staging]

  - id: deploy-production
    agent: deploy-agent
    needs: [integration-tests]
    approval: required
    approval_timeout: 4h
    inputs:
      artifact: "{{steps.build.outputs.artifact_url}}"
```

In this example, the production deployment step will pause and require approval before proceeding. If no one approves within 4 hours, the workflow will be cancelled.
