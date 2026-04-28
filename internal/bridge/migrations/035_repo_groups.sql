CREATE TABLE IF NOT EXISTS repo_groups (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    source_repo TEXT NOT NULL DEFAULT '',
    source_file TEXT NOT NULL DEFAULT '',
    source_key  TEXT NOT NULL DEFAULT '',
    raw_yaml    TEXT DEFAULT '',
    parsed      JSONB,
    sync_error  TEXT DEFAULT '',
    last_synced TIMESTAMPTZ,
    team_id     TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_key, team_id)
);
CREATE INDEX IF NOT EXISTS idx_repo_groups_team ON repo_groups(team_id);
CREATE INDEX IF NOT EXISTS idx_repo_groups_name ON repo_groups(name, team_id);
