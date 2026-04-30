-- Add sync_warning column to agent_definitions table for credential gap warnings
ALTER TABLE agent_definitions ADD COLUMN sync_warning TEXT;