-- 041_workflow_retry_count.sql
-- Add retry_count column to workflow_run_steps for output contract violation retries.

ALTER TABLE workflow_run_steps ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;