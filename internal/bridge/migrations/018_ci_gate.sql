-- CI Gate state tracking for Bridge-driven CI retry loop.
CREATE TABLE IF NOT EXISTS ci_gate_state (
    session_id TEXT PRIMARY KEY,
    pr_repo TEXT NOT NULL,
    pr_number INT NOT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    status TEXT NOT NULL DEFAULT 'monitoring',
    original_session_id TEXT NOT NULL,
    task_def_source_key TEXT,
    owner TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
