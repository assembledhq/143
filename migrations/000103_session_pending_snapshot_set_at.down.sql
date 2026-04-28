DROP INDEX IF EXISTS idx_sessions_pending_snapshot_set_at;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS pending_snapshot_set_at;
