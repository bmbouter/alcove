ALTER TABLE auth_users ALTER COLUMN password DROP NOT NULL;
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS external_id TEXT;
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS display_name TEXT;
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS auth_source TEXT DEFAULT 'local';
CREATE UNIQUE INDEX IF NOT EXISTS idx_auth_users_external_id ON auth_users(external_id) WHERE external_id IS NOT NULL;
