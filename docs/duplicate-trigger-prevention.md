# Duplicate Trigger Prevention

This document explains the fix for issue #480 and how to prevent duplicate trigger configurations.

## Problem

Previously, both agent definitions and workflow definitions could contain `trigger:` configurations. This caused conflicts where:

1. An agent might be triggered directly via its own trigger definition
2. The same agent might also be invoked by a workflow with its own trigger
3. This created duplicate executions and unpredictable behavior

## Solution

**Agents should NOT have trigger definitions.** Only workflows should define triggers.

- **Agent files** (`.alcove/agents/*.yml`) should NOT contain `trigger:` sections
- **Workflow files** (`.alcove/workflows/*.yml`) are the only place triggers should be defined
- Agent descriptions should mention they are "invoked by" workflows, not "triggered by" events

## Example

### ❌ Incorrect (agent with trigger)
```yaml
name: My Agent
trigger:
  github:
    events: [issues]
    labels: [ready-for-dev]
description: Triggered by ready-for-dev label
```

### ✅ Correct (workflow with trigger, agent without)
```yaml
# .alcove/workflows/my-workflow.yml
name: My Workflow
trigger:
  github:
    events: [issues] 
    labels: [ready-for-dev]
workflow:
  - type: agent
    agent: My Agent
```

```yaml
# .alcove/agents/my-agent.yml  
name: My Agent
description: Invoked by My Workflow when issues are labeled ready-for-dev
```

## Validation

Run `./scripts/validate-triggers.sh` to check for duplicate triggers. This is automatically run during `make test`.

## References

- Issue #480: Remove duplicate trigger from autonomous-dev agent
- Commit c6aaaf4: Original fix removing triggers from agent definitions
