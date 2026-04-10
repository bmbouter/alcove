-- Persistent deduplication for polled events.
-- Prevents the same issue/PR + schedule from being dispatched twice
-- when multiple GitHub events (e.g. opened + labeled) arrive across
-- different poll cycles.
CREATE TABLE IF NOT EXISTS dispatched_dedup (
    repo TEXT NOT NULL,
    item_number TEXT NOT NULL,
    schedule_id TEXT NOT NULL,
    dispatched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (repo, item_number, schedule_id)
);

CREATE INDEX IF NOT EXISTS idx_dispatched_dedup_at ON dispatched_dedup(dispatched_at);
