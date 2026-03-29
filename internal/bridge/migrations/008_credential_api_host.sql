-- 008_credential_api_host.sql
-- Add api_host column to provider_credentials for self-hosted service instances.

ALTER TABLE provider_credentials
    ADD COLUMN IF NOT EXISTS api_host TEXT NOT NULL DEFAULT '';
