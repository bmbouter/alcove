ALTER TABLE schedules ADD COLUMN IF NOT EXISTS trigger_type TEXT DEFAULT 'cron';
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS event_config JSONB;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    delivery_id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    repo TEXT NOT NULL,
    action TEXT DEFAULT '',
    matched_schedules INTEGER DEFAULT 0,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_received ON webhook_deliveries(received_at);
