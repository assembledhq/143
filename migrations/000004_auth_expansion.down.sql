DROP INDEX IF EXISTS idx_users_google_id;
ALTER TABLE users DROP COLUMN IF EXISTS google_id;
ALTER TABLE users DROP COLUMN IF EXISTS password_hash;
