-- Add repos JSONB column to sessions, migrate existing data, drop repo
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS repos JSONB;
UPDATE sessions SET repos = CASE
    WHEN repo IS NOT NULL AND repo != '' THEN jsonb_build_array(jsonb_build_object('url', repo))
    ELSE '[]'::jsonb
END WHERE repos IS NULL;
ALTER TABLE sessions DROP COLUMN IF EXISTS repo;

-- Add repos JSONB column to schedules, migrate existing data, drop repo
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS repos JSONB;
UPDATE schedules SET repos = CASE
    WHEN repo IS NOT NULL AND repo != '' THEN jsonb_build_array(jsonb_build_object('url', repo))
    ELSE '[]'::jsonb
END WHERE repos IS NULL;
ALTER TABLE schedules DROP COLUMN IF EXISTS repo;
