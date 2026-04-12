-- Rename task_repos settings key to agent_repos
UPDATE user_settings SET key = 'agent_repos' WHERE key = 'task_repos';

-- Rename task_definitions table to agent_definitions
ALTER TABLE IF EXISTS task_definitions RENAME TO agent_definitions;
