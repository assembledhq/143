-- Reverse 000076_sessions_mru_ordering.up.sql.
-- Drops the MRU index, restores the old created_at-ordered partial index,
-- relaxes last_activity_at back to nullable with no default, and drops the
-- helper CHECK if the production runbook left it behind. Backfilled
-- timestamps are left in place (harmless).

DROP INDEX IF EXISTS idx_sessions_last_activity;

CREATE INDEX IF NOT EXISTS idx_sessions_deleted
    ON sessions (org_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP NOT NULL;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP DEFAULT;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_last_activity_at_not_null;
