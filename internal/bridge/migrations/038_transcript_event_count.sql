-- 038_transcript_event_count.sql
-- Add transcript_event_count column for observability of empty-transcript sessions.

-- Add the new column with a default of 0
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS transcript_event_count INT DEFAULT 0;

-- Backfill existing sessions with transcript event counts
-- This uses jsonb_array_length to count existing transcript events
UPDATE sessions
SET transcript_event_count = jsonb_array_length(COALESCE(transcript, '[]'::jsonb))
WHERE transcript_event_count IS NULL OR transcript_event_count = 0;
