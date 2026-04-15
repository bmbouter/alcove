-- 028_workflow_graph_v2.sql
-- Add iteration tracking for bounded cycles in workflow steps.

ALTER TABLE workflow_run_steps ADD COLUMN iteration INTEGER NOT NULL DEFAULT 0;
