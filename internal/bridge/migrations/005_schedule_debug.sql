-- 005_schedule_debug.sql
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS debug BOOLEAN NOT NULL DEFAULT false;
