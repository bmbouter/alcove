-- 001_initial_schema.sql
-- Creates all Alcove tables.

CREATE TABLE IF NOT EXISTS sessions (
    id            UUID PRIMARY KEY,
    task_id       UUID NOT NULL,
    submitter     TEXT NOT NULL,
    prompt        TEXT NOT NULL,
    scope         JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider      TEXT NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    finished_at   TIMESTAMPTZ,
    exit_code     INT,
    outcome       TEXT,
    transcript    JSONB,
    proxy_log     JSONB,
    artifacts     JSONB,
    parent_id     UUID REFERENCES sessions(id)
);

CREATE TABLE IF NOT EXISTS provider_credentials (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    provider    TEXT NOT NULL,
    auth_type   TEXT NOT NULL,
    credential  BYTEA NOT NULL,
    project_id  TEXT,
    region      TEXT DEFAULT 'us-east5',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_users (
    username    TEXT PRIMARY KEY,
    password    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
    token       TEXT PRIMARY KEY,
    username    TEXT NOT NULL REFERENCES auth_users(username) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);

CREATE TABLE IF NOT EXISTS schedules (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL,
    cron         TEXT NOT NULL,
    prompt       TEXT NOT NULL,
    repo         TEXT,
    provider     TEXT,
    scope_preset TEXT,
    timeout      INT DEFAULT 3600,
    enabled      BOOLEAN DEFAULT true,
    last_run     TIMESTAMPTZ,
    next_run     TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
