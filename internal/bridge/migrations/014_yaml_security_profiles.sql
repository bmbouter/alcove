-- 014_yaml_security_profiles.sql
-- Add source tracking columns to security_profiles for YAML-defined profiles.

ALTER TABLE security_profiles ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'user';
ALTER TABLE security_profiles ADD COLUMN IF NOT EXISTS source_repo TEXT NOT NULL DEFAULT '';
ALTER TABLE security_profiles ADD COLUMN IF NOT EXISTS source_key TEXT NOT NULL DEFAULT '';

-- Remove builtin profiles (replaced by YAML-sourced profiles).
DELETE FROM security_profiles WHERE is_builtin = true;

-- Unique index on source_key for YAML-sourced profiles (used for upsert conflict target).
CREATE UNIQUE INDEX IF NOT EXISTS idx_security_profiles_source_key
    ON security_profiles(source_key) WHERE source_key != '';
