-- Add workflow context to sessions table to support workflow/step identification.

-- Add workflow context fields to sessions
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS workflow_run_id TEXT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS workflow_run_step_id TEXT;

-- Add indexes for workflow filtering
CREATE INDEX IF NOT EXISTS idx_sessions_workflow_run_id ON sessions(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_sessions_workflow_run_step_id ON sessions(workflow_run_step_id);

-- Add foreign key constraints to ensure referential integrity
ALTER TABLE sessions ADD CONSTRAINT fk_sessions_workflow_run_id
    FOREIGN KEY (workflow_run_id) REFERENCES workflow_runs(id) ON DELETE SET NULL;
ALTER TABLE sessions ADD CONSTRAINT fk_sessions_workflow_run_step_id
    FOREIGN KEY (workflow_run_step_id) REFERENCES workflow_run_steps(id) ON DELETE SET NULL;