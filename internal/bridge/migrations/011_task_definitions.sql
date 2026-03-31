ALTER TABLE schedules ADD COLUMN IF NOT EXISTS source TEXT DEFAULT 'manual';
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS source_key TEXT;

CREATE TABLE IF NOT EXISTS task_definitions (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    source_repo TEXT NOT NULL,
    source_file TEXT NOT NULL,
    source_key TEXT NOT NULL UNIQUE,
    raw_yaml TEXT NOT NULL,
    parsed JSONB NOT NULL,
    has_schedule BOOLEAN DEFAULT false,
    sync_error TEXT,
    last_synced TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
