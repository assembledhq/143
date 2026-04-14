DROP INDEX IF EXISTS idx_sessions_not_archived;
ALTER TABLE sessions DROP COLUMN IF EXISTS archived_by_user_id;
ALTER TABLE sessions DROP COLUMN IF EXISTS archived_at;
