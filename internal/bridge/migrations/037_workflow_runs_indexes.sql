-- Add database indexes for workflow runs pagination and filtering.

-- Composite index for (team_id, created_at) for date-based filtering with pagination
CREATE INDEX IF NOT EXISTS idx_workflow_runs_team_created
ON workflow_runs(team_id, created_at);

-- Index for (team_id, trigger_ref) for trigger ref search
CREATE INDEX IF NOT EXISTS idx_workflow_runs_team_trigger_ref
ON workflow_runs(team_id, trigger_ref);

-- Composite index for (team_id, status, created_at) for status filtering with date ordering
CREATE INDEX IF NOT EXISTS idx_workflow_runs_team_status_created
ON workflow_runs(team_id, status, created_at);