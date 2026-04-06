-- 016_poll_state.sql
-- Track GitHub Events API polling state per repository.

CREATE TABLE IF NOT EXISTS github_poll_state (
    repo TEXT PRIMARY KEY,
    etag TEXT DEFAULT '',
    last_event_id TEXT DEFAULT '',
    last_polled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
