-- 034_profile_rules.sql
-- Add rules column to security_profiles for HTTP-primitive policy rules.
ALTER TABLE security_profiles ADD COLUMN IF NOT EXISTS rules JSONB NOT NULL DEFAULT '[]';
