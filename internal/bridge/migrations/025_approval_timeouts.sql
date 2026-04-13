-- Add approval timeout support to workflow run steps.

-- Add approval timeout column to track when approval times out
ALTER TABLE workflow_run_steps ADD COLUMN IF NOT EXISTS approval_timeout_at TIMESTAMPTZ;

-- Index for efficient approval timeout queries
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_approval_timeout ON workflow_run_steps(approval_timeout_at) WHERE approval_timeout_at IS NOT NULL;
