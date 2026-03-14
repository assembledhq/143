DROP INDEX IF EXISTS idx_sessions_triggered_by;
ALTER TABLE sessions DROP COLUMN IF EXISTS triggered_by_user_id;
