# Workflow Runs Pagination and Filtering

This document describes the enhanced workflow runs API and CLI with pagination, filtering, and status summary features implemented for issue #504.

## API Changes

### New Query Parameters for GET /api/v1/workflow-runs

- `limit` (int, default 25, max 200) - Number of results per page
- `offset` (int, default 0) - Number of results to skip 
- `workflow` (string) - Filter by workflow name (partial match with ILIKE)
- `since` (string) - Filter by date: "1d", "7d", "30d", or ISO date (YYYY-MM-DD)
- `search` (string) - Search by trigger_ref (exact match)
- `summary` (bool) - Include status counts in response

### Enhanced Response Format

```json
{
  "workflow_runs": [...],
  "count": 25,
  "total": 150,
  "summary": {
    "running": 3,
    "pending": 2, 
    "completed": 12,
    "failed": 1,
    "cancelled": 0,
    "awaiting_approval": 1
  }
}
```

### Backward Compatibility

- Legacy API calls (no new parameters) use the original format and behavior
- Default limit is 25 (increased from hardcoded 100)
- Original response structure preserved when `summary=false`

## CLI Changes

### Enhanced `alcove workflows runs` Command

New flags:
- `--limit INT` - Number of results per page (default 25, max 200)
- `--offset INT` - Number of results to skip (default 0)  
- `--workflow STRING` - Filter by workflow name (partial match)
- `--since STRING` - Filter by date: 1d, 7d, 30d, or YYYY-MM-DD
- `--search STRING` - Search by trigger ref (exact match)
- `--summary` - Include status summary

### Examples

```bash
# List recent runs (default 25)
alcove workflows runs

# Pagination
alcove workflows runs --limit 50 --offset 25

# Filter by status
alcove workflows runs --status failed

# Filter by workflow name
alcove workflows runs --workflow "SDLC Pipeline"

# Last 7 days
alcove workflows runs --since 7d

# Search by trigger ref  
alcove workflows runs --search "owner/repo#42"

# Include status summary
alcove workflows runs --summary
```

### Enhanced Output

- Status summary bar when `--summary` used
- Pagination info ("Showing 1-25 of 150 workflow runs")
- Preserved tabular format for compatibility

## Database Changes

### New Migration (037_workflow_runs_indexes.sql)

Adds composite indexes for efficient filtering and pagination:

- `idx_workflow_runs_team_created` - (team_id, created_at) for date-based filtering
- `idx_workflow_runs_team_trigger_ref` - (team_id, trigger_ref) for trigger ref search  
- `idx_workflow_runs_team_status_created` - (team_id, status, created_at) for status filtering

All indexes use `CREATE INDEX CONCURRENTLY` to avoid locks during deployment.

## Implementation Details

### Key Components

1. **WorkflowRunsFilter struct** - Encapsulates filtering options with validation
2. **Enhanced ListWorkflowRuns** - Supports pagination and advanced filtering with JOIN
3. **GetWorkflowRunsSummary** - Calculates status counts for filtered results
4. **Legacy compatibility** - `ListWorkflowRunsLegacy` preserves old behavior

### Security Considerations

- All queries maintain team_id scoping for isolation
- Date range filtering with bounds checking  
- Trigger ref search uses exact match to prevent SQL injection
- ILIKE for workflow name uses parameterized queries
- Maximum page size limit (200) prevents memory exhaustion

### Performance Optimizations

- Composite indexes for efficient filtering
- JOIN with workflows table only when workflow name filtering needed
- Status summary calculated from same filtered dataset
- Query parameter validation prevents expensive operations

## Testing

Unit tests cover:
- Date parsing for relative terms (1d, 7d, 30d) and ISO dates
- Filter validation (limits, offsets, team_id requirement)
- Error handling for invalid parameters
- Backward compatibility with legacy API calls

## Migration Path

1. Deploy database migration (037_workflow_runs_indexes.sql)
2. Deploy Bridge with enhanced API handlers
3. Update CLI tools to latest version
4. Existing scripts continue to work unchanged
5. New features available via new query parameters