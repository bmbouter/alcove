# Credential Validation for Agents

## Overview

This feature validates that agent definitions have the required credentials available at both sync time and dispatch time, providing clear feedback when credentials are missing.

## Problem Solved

Previously, agents would be dispatched successfully even when required credentials were missing, leading to silent failures at runtime. Users would see "completed" sessions with confusing error messages like "permission restrictions" without any indication that the root cause was a missing credential configuration.

## How It Works

### Sync Time Validation

When agent repositories are synced, Bridge validates that every agent's security profile operations can be satisfied by the team's configured credentials:

1. **Profile Resolution**: For each agent definition, resolve its security profiles
2. **Credential Requirements**: Extract tool service requirements (github, gitlab, jira, splunk) from profile tools
3. **Credential Check**: Query available team credentials to verify required services are configured
4. **Warning Storage**: Store `sync_warning` on agent definitions with missing credential types

### Dispatch Time Validation  

When a session is dispatched, Bridge performs real-time credential validation:

1. **Pre-dispatch Check**: Validate credentials before creating the session
2. **Warning Response**: Include warnings in the API response (agents remain dispatchable)
3. **CLI Display**: Show warnings to stderr before displaying the session ID

### Warning Format

```
missing credentials: github (required by profile 'alcove-releaser' for operations [push_branch, clone])
```

## CLI Interface

### Agents List

Shows a WARNING column when agents have credential gaps:

```bash
$ alcove agents list
NAME                        STATUS    WARNING                              SOURCE
Automated Release Agent     synced    ⚠ missing github credential         https://github.com/team/agents
Code Review Agent           synced                                         https://github.com/team/agents

⚠ 1 agent(s) have unmet credential requirements. Run `alcove agents check-credentials` for details.
```

### Credential Check Command

Provides detailed analysis of credential requirements:

```bash
$ alcove agents check-credentials
Found 1 agent(s) with unmet credential requirements:

AGENT                      SOURCE_FILE                WARNING
Automated Release Agent    release-agent.yml          missing credentials: github (required by profile 'alcove-releaser' for operations [push_branch, clone])

To resolve these issues:
1. Run `alcove credentials list` to see available credentials
2. Run `alcove credentials create` to add missing credentials  
3. Run `alcove agents sync` to refresh agent definitions
```

### Dispatch Warnings

When running agents with missing credentials:

```bash
$ alcove agents run "Release Agent"
⚠ Warning: agent requires github operations [push_branch, clone] but no github credential is configured

Session dispatched: sess_123456789
```

## API Changes

### Agent Definitions Response

Agent definitions now include the `sync_warning` field:

```json
{
  "id": "agent_123",
  "name": "Release Agent", 
  "sync_warning": "missing credentials: github (required by profile 'alcove-releaser' for operations [push_branch, clone])"
}
```

### Dispatch Response

The dispatch API now returns warnings alongside session information:

```json
{
  "session_id": "sess_123456789",
  "status": "running",
  "started_at": "2026-04-29T10:00:00Z",
  "warnings": [
    "agent requires github operations [push_branch, clone] but no github credential is configured"
  ]
}
```

## Database Schema

### Migration 038: Add sync_warning Column

```sql
ALTER TABLE agent_definitions ADD COLUMN sync_warning TEXT;
```

The `sync_warning` column stores credential gap messages:
- **NULL/Empty**: No credential issues
- **Non-empty**: Description of missing credential requirements

## Design Decisions

### Warnings vs Errors

- **`sync_error`**: Parse errors, unknown profiles (blocks dispatch)
- **`sync_warning`**: Missing credentials (allows dispatch with warning)

Rationale: Credentials can be added between sync cycles, some agents may do useful work without gated operations, avoids breaking automation.

### Validation Granularity

Only validates credential **type** existence (github, gitlab, jira, splunk), not specific permission scopes. Scope validation would require calling external APIs and is handled at runtime by the proxy layer.

### Performance

- Single batched query per team during sync: `SELECT DISTINCT provider FROM provider_credentials WHERE team_id = $1`
- Credential type set cached for team's sync batch
- Negligible runtime impact

## Implementation Status

✅ **Step 1**: Database schema and model changes  
✅ **Step 2**: Sync-time validation logic  
✅ **Step 3**: CLI warnings and check-credentials command  
✅ **Step 4**: Dispatch-time warnings  
⏸️ **Step 5**: Dashboard warning indicators (UI changes)  
⏸️ **Step 6**: Documentation updates  
⏸️ **Step 7**: Comprehensive integration tests  

## Future Enhancements

- **Team Setting**: `block_dispatch_on_credential_gap` for hard blocking behavior
- **Scope Validation**: Verify specific permission levels (read vs write)
- **Credential Health**: Periodic validation of stored credentials
- **Auto-remediation**: Suggest specific credential creation commands