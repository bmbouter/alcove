CREATE TABLE IF NOT EXISTS personal_api_tokens (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    token_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_accessed_at TIMESTAMPTZ,
    FOREIGN KEY (username) REFERENCES auth_users(username) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_personal_api_tokens_username ON personal_api_tokens(username);