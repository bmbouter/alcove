-- System state for maintenance mode.
CREATE TABLE IF NOT EXISTS system_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Default to active mode.
INSERT INTO system_state (key, value) VALUES ('mode', 'active')
ON CONFLICT (key) DO NOTHING;
