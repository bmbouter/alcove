-- 027_teams.sql
-- Add teams for multi-user resource sharing.

-- 1. Create teams tables.

CREATE TABLE teams (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    is_personal BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE team_members (
    team_id  UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    PRIMARY KEY (team_id, username)
);
CREATE INDEX idx_team_members_username ON team_members(username);

CREATE TABLE team_settings (
    team_id    UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, key)
);

-- 2. Create a personal team for each existing user.

INSERT INTO teams (id, name, is_personal, created_at)
SELECT gen_random_uuid(), username || '''s workspace', true, NOW()
FROM auth_users;

INSERT INTO team_members (team_id, username)
SELECT t.id, u.username
FROM auth_users u
JOIN teams t ON t.name = u.username || '''s workspace' AND t.is_personal = true;

-- 3. Add team_id column (nullable) to resource tables.

ALTER TABLE sessions ADD COLUMN team_id UUID;
ALTER TABLE provider_credentials ADD COLUMN team_id UUID;
ALTER TABLE security_profiles ADD COLUMN team_id UUID;
ALTER TABLE agent_definitions ADD COLUMN team_id UUID;
ALTER TABLE schedules ADD COLUMN team_id UUID;
ALTER TABLE workflows ADD COLUMN team_id UUID;
ALTER TABLE workflow_runs ADD COLUMN team_id UUID;
ALTER TABLE mcp_tools ADD COLUMN team_id UUID;

-- 4. Backfill team_id from owner/submitter via personal teams.

UPDATE sessions s
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE s.submitter = tm.username;

UPDATE provider_credentials pc
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE pc.owner = tm.username;

-- Handle system credentials (owner = '_system' or owner = '') — assign to first team or leave NULL
UPDATE provider_credentials SET team_id = NULL WHERE owner = '_system' OR owner = '';

UPDATE security_profiles sp
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE sp.owner = tm.username;

UPDATE agent_definitions ad
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE ad.owner = tm.username;

UPDATE schedules sc
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE sc.owner = tm.username;

UPDATE workflows w
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE w.owner = tm.username;

UPDATE workflow_runs wr
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE wr.owner = tm.username;

UPDATE mcp_tools mt
SET team_id = t.id
FROM team_members tm
JOIN teams t ON tm.team_id = t.id AND t.is_personal = true
WHERE mt.owner = tm.username;

-- 5. Set team_id to NOT NULL (except provider_credentials which has _system rows)
--    and add foreign key constraints.

-- For tables that may have NULL team_id (system credentials), allow nullable
ALTER TABLE sessions ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE sessions ADD CONSTRAINT fk_sessions_team FOREIGN KEY (team_id) REFERENCES teams(id);

-- provider_credentials: system credentials have no team, so keep nullable
ALTER TABLE provider_credentials ADD CONSTRAINT fk_provider_credentials_team FOREIGN KEY (team_id) REFERENCES teams(id);

ALTER TABLE security_profiles ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE security_profiles ADD CONSTRAINT fk_security_profiles_team FOREIGN KEY (team_id) REFERENCES teams(id);

ALTER TABLE agent_definitions ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE agent_definitions ADD CONSTRAINT fk_agent_definitions_team FOREIGN KEY (team_id) REFERENCES teams(id);

ALTER TABLE schedules ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE schedules ADD CONSTRAINT fk_schedules_team FOREIGN KEY (team_id) REFERENCES teams(id);

ALTER TABLE workflows ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE workflows ADD CONSTRAINT fk_workflows_team FOREIGN KEY (team_id) REFERENCES teams(id);

ALTER TABLE workflow_runs ALTER COLUMN team_id SET NOT NULL;
ALTER TABLE workflow_runs ADD CONSTRAINT fk_workflow_runs_team FOREIGN KEY (team_id) REFERENCES teams(id);

-- mcp_tools: team_id is nullable for builtin tools (team_id IS NULL means builtin/global).
ALTER TABLE mcp_tools ADD CONSTRAINT fk_mcp_tools_team FOREIGN KEY (team_id) REFERENCES teams(id);

-- 6. Drop owner columns from resource tables (keep submitter on sessions).

ALTER TABLE provider_credentials DROP COLUMN owner;
ALTER TABLE security_profiles DROP COLUMN owner;
ALTER TABLE agent_definitions DROP COLUMN owner;
ALTER TABLE schedules DROP COLUMN owner;
ALTER TABLE workflows DROP COLUMN owner;
ALTER TABLE workflow_runs DROP COLUMN owner;
ALTER TABLE mcp_tools DROP COLUMN owner;

-- 7. Recreate unique constraints with team_id.

-- security_profiles: drop old unique index and create new one
DROP INDEX IF EXISTS idx_security_profiles_name_owner;
CREATE UNIQUE INDEX idx_security_profiles_name_team ON security_profiles(name, team_id);

-- mcp_tools: drop old unique index and create new ones
DROP INDEX IF EXISTS idx_mcp_tools_name_owner;
CREATE UNIQUE INDEX idx_mcp_tools_name_team ON mcp_tools(name, team_id) WHERE team_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mcp_tools_name_builtin ON mcp_tools(name) WHERE team_id IS NULL;

-- agent_definitions: change UNIQUE(source_key) to UNIQUE(source_key, team_id)
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS agent_definitions_source_key_key;
ALTER TABLE agent_definitions DROP CONSTRAINT IF EXISTS task_definitions_source_key_key;
CREATE UNIQUE INDEX idx_agent_definitions_source_key_team ON agent_definitions(source_key, team_id);

-- workflows: change UNIQUE(source_key) to UNIQUE(source_key, team_id)
ALTER TABLE workflows DROP CONSTRAINT IF EXISTS workflows_source_key_key;
CREATE UNIQUE INDEX idx_workflows_source_key_team ON workflows(source_key, team_id);

-- Add indexes for team_id on frequently queried tables.
CREATE INDEX idx_sessions_team ON sessions(team_id);
CREATE INDEX idx_provider_credentials_team ON provider_credentials(team_id);
CREATE INDEX idx_security_profiles_team ON security_profiles(team_id);
CREATE INDEX idx_agent_definitions_team ON agent_definitions(team_id);
CREATE INDEX idx_schedules_team ON schedules(team_id);
CREATE INDEX idx_workflows_team ON workflows(team_id);
CREATE INDEX idx_workflow_runs_team ON workflow_runs(team_id);
CREATE INDEX idx_mcp_tools_team ON mcp_tools(team_id);

-- Drop old owner indexes.
DROP INDEX IF EXISTS idx_provider_credentials_owner;
DROP INDEX IF EXISTS idx_schedules_owner;
DROP INDEX IF EXISTS idx_workflows_owner;

-- 8. Migrate user_settings agent repo entries to team_settings.

INSERT INTO team_settings (team_id, key, value, updated_at)
SELECT t.id, us.key, us.value, us.updated_at
FROM user_settings us
JOIN team_members tm ON us.username = tm.username
JOIN teams t ON tm.team_id = t.id
WHERE t.is_personal = true;
