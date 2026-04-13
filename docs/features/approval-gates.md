# Approval Gates (Human-in-the-Loop)

Workflow steps can require human approval before executing, providing manual oversight for critical operations like production deployments.

## YAML Configuration

Add an `approval: required` field to any workflow step:

```yaml
name: Feature Delivery Pipeline
workflow:
  - id: implement
    agent: autonomous-developer
    repo: pulp/pulp_python
    outputs: [pr_url, summary]

  - id: deploy-staging
    agent: deploy-agent
    needs: [implement]
    condition: "steps.implement.outcome == 'completed'"

  - id: promote-prod
    agent: deploy-agent
    needs: [deploy-staging]
    approval: required              # Human approval required
    approval_timeout: "72h"         # Optional: custom timeout (default: 72h)
    inputs:
      context: "{{steps.deploy-staging.outputs.summary}}"
```

## How It Works

1. **Workflow Execution**: When the engine reaches a step with `approval: required`, it:
   - Sets the step status to `awaiting_approval`
   - Sets the workflow run status to `awaiting_approval`
   - Calculates a timeout timestamp (default: 72 hours)
   - Pauses execution until human intervention

2. **Dashboard UI**: The dashboard shows:
   - A prominent approval card for pending approvals
   - Approve/Reject buttons with workflow context
   - Timeout information
   - Step details and dependencies

3. **Approval Resolution**:
   - **Approved**: Step marked as `completed`, workflow continues
   - **Rejected**: Step marked as `failed`, workflow marked as `failed`
   - **Timeout**: Step marked as `failed`, workflow marked as `cancelled`

## API Endpoints

### Get Workflow Run Details
```bash
GET /api/v1/workflow-runs/{id}
```

Response includes pending approvals:
```json
{
  "workflow_run": { ... },
  "steps": [ ... ],
  "pending_approvals": [
    {
      "step_id": "promote-prod",
      "started_at": "2026-04-13T10:00:00Z"
    }
  ]
}
```

### Approve a Step
```bash
POST /api/v1/workflow-runs/{run_id}/steps/{step_id}/approve
```

### Reject a Step
```bash
POST /api/v1/workflow-runs/{run_id}/steps/{step_id}/reject
```

## Timeout Configuration

Approval timeouts prevent workflows from hanging indefinitely:

```yaml
- id: critical-deployment
  agent: deploy-agent
  approval: required
  approval_timeout: "4h"    # Shorter timeout for urgent deployments
```

Supported timeout formats:
- `"1h"` - 1 hour
- `"30m"` - 30 minutes
- `"72h"` - 72 hours (default)
- `"7d"` - 7 days

## Budget-Based Approval Gates

Consider using approval gates for expensive operations. For example, if an agent might consume significant compute or API costs, require approval before proceeding:

```yaml
- id: expensive-analysis
  agent: deep-analyzer
  approval: required
  approval_timeout: "1h"
  inputs:
    budget_warning: "This step may cost $50+ in API calls"
```

## Best Practices

1. **Use Sparingly**: Only require approval for critical steps (production deployments, destructive operations)

2. **Clear Context**: Provide enough information in step inputs for informed decisions:
   ```yaml
   inputs:
     summary: "{{steps.test.outputs.results}}"
     impact: "Production deployment affects 1000+ users"
     rollback_plan: "{{steps.plan.outputs.rollback_procedure}}"
   ```

3. **Reasonable Timeouts**: Balance urgency with availability:
   - Production issues: 1-4 hours
   - Regular deployments: 24-72 hours
   - Major releases: 1 week

4. **Team Coverage**: Ensure multiple team members can approve critical workflows

## Integration with External Systems

Future enhancements may include:
- Slack/Teams notifications for pending approvals
- Email alerts with approve/reject links
- Integration with existing approval systems (JIRA, ServiceNow)
- Role-based approval (only admins can approve production deployments)

## Monitoring

The system automatically tracks:
- Approval response times
- Timeout frequency
- Approval patterns by user/time

Use these metrics to optimize timeout settings and team processes.
