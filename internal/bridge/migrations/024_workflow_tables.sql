-- Workflow orchestration tables for multi-step agent coordination.

-- Workflows store the YAML definitions and metadata.
CREATE TABLE IF NOT EXISTS workflows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_repo TEXT,
    source_file TEXT,
    source_key TEXT UNIQUE,
    raw_yaml TEXT,
    parsed JSONB,
    definition JSONB,
    sync_error TEXT,
    last_synced TIMESTAMPTZ DEFAULT NOW(),
    owner TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Workflow runs track individual executions of workflows.
CREATE TABLE IF NOT EXISTS workflow_runs (
    id TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    trigger_type TEXT,
    trigger_ref TEXT,
    current_step TEXT,
    step_outputs JSONB DEFAULT '{}',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    owner TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- Workflow run steps track individual step executions within a run.
CREATE TABLE IF NOT EXISTS workflow_run_steps (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    session_id TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    outputs JSONB,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

-- Indexes for efficient querying.
CREATE INDEX IF NOT EXISTS idx_workflows_source_key ON workflows(source_key);
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);
CREATE INDEX IF NOT EXISTS idx_workflows_repo ON workflows(source_repo, owner);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow_id ON workflow_runs(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_status ON workflow_runs(status);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_owner ON workflow_runs(owner);
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_run_id ON workflow_run_steps(run_id);
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_session_id ON workflow_run_steps(session_id);
CREATE INDEX IF NOT EXISTS idx_workflow_run_steps_status ON workflow_run_steps(status);
