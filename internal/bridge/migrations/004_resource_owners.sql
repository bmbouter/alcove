-- 004_resource_owners.sql
-- Add owner columns for per-user isolation.
ALTER TABLE provider_credentials ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_provider_credentials_owner ON provider_credentials(owner);
CREATE INDEX IF NOT EXISTS idx_schedules_owner ON schedules(owner);
