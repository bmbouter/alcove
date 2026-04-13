-- Workflow orchestration tables for multi-step agent coordination.

-- Workflows store the YAML definitions and metadata.
CREATE TABLE workflows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_repo TEXT,
    source_file TEXT,
    source_key TEXT UNIQUE,
    definition JSONB NOT NULL,
    owner TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Workflow runs track individual executions of workflows.
CREATE TABLE workflow_runs (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id),
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, completed, failed, cancelled, awaiting_approval
    trigger_type TEXT,
    trigger_ref TEXT,
    current_step TEXT,
    step_outputs JSONB DEFAULT '{}',  -- accumulated outputs from completed steps
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    owner TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Workflow run steps track individual step executions within a run.
CREATE TABLE workflow_run_steps (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id),
    step_id TEXT NOT NULL,  -- matches workflow YAML step id
    session_id TEXT,  -- links to sessions table when dispatched
    status TEXT NOT NULL DEFAULT 'pending',  -- pending, running, completed, failed, skipped, awaiting_approval
    outputs JSONB,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

-- Indexes for efficient querying.
CREATE INDEX idx_workflows_source_key ON workflows(source_key);
CREATE INDEX idx_workflows_owner ON workflows(owner);
CREATE INDEX idx_workflow_runs_workflow_id ON workflow_runs(workflow_id);
CREATE INDEX idx_workflow_runs_status ON workflow_runs(status);
CREATE INDEX idx_workflow_runs_owner ON workflow_runs(owner);
CREATE INDEX idx_workflow_run_steps_run_id ON workflow_run_steps(run_id);
CREATE INDEX idx_workflow_run_steps_session_id ON workflow_run_steps(session_id);
CREATE INDEX idx_workflow_run_steps_status ON workflow_run_steps(status);