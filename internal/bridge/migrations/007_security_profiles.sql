-- 007_security_profiles.sql
CREATE TABLE IF NOT EXISTS security_profiles (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    tools        JSONB NOT NULL DEFAULT '{}',
    owner        TEXT NOT NULL DEFAULT '',
    is_builtin   BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_security_profiles_name_owner ON security_profiles(name, owner);
