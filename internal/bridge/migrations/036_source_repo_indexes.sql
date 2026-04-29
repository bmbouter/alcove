-- Optimize source_repo lookups for orphaned resource cleanup
-- These indexes improve performance for the enhanced cleanup logic that queries
-- distinct source_repo values across multiple tables.

-- Workflows table already has index from 024_workflow_tables.sql: idx_workflows_repo
-- Security profiles don't have source_repo index yet
CREATE INDEX IF NOT EXISTS idx_security_profiles_source_repo ON security_profiles(source_repo, team_id);

-- Policy rule sets don't have source_repo index yet
CREATE INDEX IF NOT EXISTS idx_policy_rule_sets_source_repo ON policy_rule_sets(source_repo, team_id);

-- Repo groups don't have source_repo index yet
CREATE INDEX IF NOT EXISTS idx_repo_groups_source_repo ON repo_groups(source_repo, team_id);

-- Agent definitions don't have source_repo index yet
CREATE INDEX IF NOT EXISTS idx_agent_definitions_source_repo ON agent_definitions(source_repo, team_id);