-- 002_session_token_and_indexes.sql
-- Add session token for ingestion auth and indexes for performance.

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS session_token TEXT;
CREATE INDEX IF NOT EXISTS idx_sessions_submitter ON sessions(submitter);
CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(session_token);
