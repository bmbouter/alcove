-- Add token and cost tracking to workflow run steps for budget monitoring.

-- Add token tracking columns to workflow_run_steps
ALTER TABLE workflow_run_steps 
ADD COLUMN IF NOT EXISTS tokens_in INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS tokens_out INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS duration_seconds INTEGER DEFAULT 0;

-- Add indexes for efficient querying by token usage
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_tokens ON workflow_run_steps(tokens_in, tokens_out);
