-- 024_workflows.sql
-- Creates workflows table for storing workflow definitions from agent repos.

CREATE TABLE IF NOT EXISTS workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    source_repo TEXT NOT NULL,
    source_file TEXT NOT NULL,
    source_key TEXT NOT NULL UNIQUE,  -- format: username::repo::filename
    raw_yaml TEXT NOT NULL,
    parsed JSONB,  -- parsed WorkflowDefinition as JSON
    sync_error TEXT,
    last_synced TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    owner TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Index for efficient queries by owner
CREATE INDEX IF NOT EXISTS idx_workflows_owner ON workflows(owner);

-- Index for efficient queries by repo
CREATE INDEX IF NOT EXISTS idx_workflows_repo ON workflows(source_repo, owner);

-- Index for finding workflows by source key
CREATE INDEX IF NOT EXISTS idx_workflows_source_key ON workflows(source_key);
