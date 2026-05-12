-- Add retry_count column to workflow_run_steps for output contract violation retries.
-- This is distinct from iteration which tracks re-execution from dependency cycles.

ALTER TABLE workflow_run_steps ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
