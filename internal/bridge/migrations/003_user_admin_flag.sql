-- 003_user_admin_flag.sql
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT false;
UPDATE auth_users SET is_admin = true
WHERE username = (SELECT username FROM auth_users ORDER BY created_at ASC LIMIT 1);
