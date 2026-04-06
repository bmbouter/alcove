-- 015_task_def_owner.sql
-- Add owner column to task_definitions for per-user scoping.
-- Clean slate YAML-synced resources — they will be recreated on next sync with correct ownership.

ALTER TABLE task_definitions ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';

-- Delete YAML-synced resources so they are recreated with per-user ownership.
DELETE FROM task_definitions;
DELETE FROM schedules WHERE source = 'yaml';
DELETE FROM security_profiles WHERE source = 'yaml';
