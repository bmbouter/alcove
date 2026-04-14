-- Add approval timeout support to workflow steps
ALTER TABLE workflow_run_steps ADD COLUMN approval_deadline TIMESTAMPTZ;

-- Index for efficient querying of approval deadlines
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_approval_deadline ON workflow_run_steps(approval_deadline) WHERE status = 'awaiting_approval';
