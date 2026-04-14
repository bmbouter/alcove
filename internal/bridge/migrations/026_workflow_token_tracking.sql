-- Add token tracking fields to workflow run steps for cost monitoring and debugging.
-- These fields support per-step token counts and duration tracking per @decko's feedback.

ALTER TABLE workflow_run_steps 
ADD COLUMN IF NOT EXISTS tokens_in INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS tokens_out INTEGER DEFAULT 0,
ADD COLUMN IF NOT EXISTS duration_seconds INTEGER DEFAULT 0;

-- Add indexes for efficient querying on token usage
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_tokens ON workflow_run_steps(tokens_in, tokens_out);
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_duration ON workflow_run_steps(duration_seconds);
