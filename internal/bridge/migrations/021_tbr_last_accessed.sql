-- 021_tbr_last_accessed.sql
-- Add last_accessed_at column to tbr_identity_associations table

ALTER TABLE tbr_identity_associations
ADD COLUMN last_accessed_at TIMESTAMPTZ;

-- Create index for performance on last_accessed_at lookups
CREATE INDEX IF NOT EXISTS idx_tbr_last_accessed
    ON tbr_identity_associations(last_accessed_at);
