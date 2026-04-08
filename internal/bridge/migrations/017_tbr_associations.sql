-- 017_tbr_associations.sql
-- Creates TBR identity association table for rh-identity auth backend

CREATE TABLE IF NOT EXISTS tbr_identity_associations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id TEXT NOT NULL REFERENCES auth_users(username) ON DELETE CASCADE,
    tbr_org_id TEXT NOT NULL,
    tbr_username TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Ensure unique TBR identity (prevent hijacking)
CREATE UNIQUE INDEX IF NOT EXISTS idx_tbr_identity_unique
    ON tbr_identity_associations(tbr_org_id, tbr_username);

-- Lookup performance for user associations
CREATE INDEX IF NOT EXISTS idx_tbr_user_id
    ON tbr_identity_associations(user_id);
