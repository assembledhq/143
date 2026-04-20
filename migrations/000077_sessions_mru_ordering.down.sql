-- Reverse 000077_sessions_mru_ordering.up.sql.
-- Drops the MRU index, relaxes last_activity_at back to nullable with no
-- default, and drops the helper CHECK if the production runbook left it
-- behind. Backfilled timestamps are left in place (harmless).
-- idx_sessions_deleted is NOT recreated here because the up migration no
-- longer drops it.

DROP INDEX IF EXISTS idx_sessions_last_activity;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP NOT NULL;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP DEFAULT;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_last_activity_at_not_null;
