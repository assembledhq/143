-- Reverse 000076_sessions_mru_ordering.up.sql.
-- Drops the MRU index and relaxes last_activity_at back to nullable with
-- no default. Backfilled timestamps are left in place (harmless).

DROP INDEX IF EXISTS idx_sessions_last_activity;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP NOT NULL;

ALTER TABLE sessions
    ALTER COLUMN last_activity_at DROP DEFAULT;
