-- Migration 040: Add GitLab poll state table for tracking polling position per project.
-- GitLab Events API doesn't support ETag but events have numeric IDs that are chronologically ordered.

CREATE TABLE IF NOT EXISTS gitlab_poll_state (
    project TEXT PRIMARY KEY,           -- URL-encoded GitLab project path (e.g., "group%2Fproject")
    last_event_id BIGINT DEFAULT 0,     -- Last processed event ID for incremental polling
    last_polled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);