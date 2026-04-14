-- Add missing constraints to workflow tables to match the original specification.

-- Add NOT NULL constraint to definition column (if any NULL values exist, this will fail)
-- First update any NULL values to empty JSON object
UPDATE workflows SET definition = '{}' WHERE definition IS NULL;
ALTER TABLE workflows
ALTER COLUMN definition SET NOT NULL;

-- Add foreign key constraints for referential integrity
-- Note: These will fail if the constraints already exist
ALTER TABLE workflow_runs
ADD CONSTRAINT fk_workflow_runs_workflow_id
FOREIGN KEY (workflow_id) REFERENCES workflows(id);

ALTER TABLE workflow_run_steps
ADD CONSTRAINT fk_workflow_run_steps_run_id
FOREIGN KEY (run_id) REFERENCES workflow_runs(id);
