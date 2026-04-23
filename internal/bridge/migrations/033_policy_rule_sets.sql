CREATE TABLE IF NOT EXISTS policy_rule_sets (
    id TEXT PRIMARY KEY DEFAULT gen_random_uuid()::TEXT,
    name TEXT NOT NULL,
    rules JSONB NOT NULL DEFAULT '[]',
    team_id TEXT NOT NULL DEFAULT '',
    source_repo TEXT NOT NULL DEFAULT '',
    source_file TEXT NOT NULL DEFAULT '',
    source_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_policy_rule_sets_name_team ON policy_rule_sets (name, team_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_policy_rule_sets_source_key ON policy_rule_sets (source_key) WHERE source_key IS NOT NULL;
