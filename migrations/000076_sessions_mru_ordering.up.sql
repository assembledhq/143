-- Switch the sessions list to Most-Recently-Updated ordering by making
-- last_activity_at the authoritative "last touched" timestamp for every
-- session and backing the new ORDER BY with a matching partial index.
--
-- NOTE: For production, create the new index CONCURRENTLY before running
-- this migration file (see migrations 000063, 000071 for prior art).

-- Backfill: existing rows may have NULL last_activity_at (the column was
-- added in 000023 without a default and is only written by turn/snapshot
-- updates). Fall back to the best available timestamp so nothing is NULL.
UPDATE sessions
SET last_activity_at = COALESCE(last_activity_at, completed_at, started_at, created_at)
WHERE last_activity_at IS NULL;

-- Enforce NOT NULL with a default so future inserts always have a value.
ALTER TABLE sessions
    ALTER COLUMN last_activity_at SET DEFAULT now();

ALTER TABLE sessions
    ALTER COLUMN last_activity_at SET NOT NULL;

-- Partial index supporting `ORDER BY last_activity_at DESC, id DESC`
-- with the `org_id` prefix and `deleted_at IS NULL` filter used by
-- SessionStore.ListByOrg. Mirrors the shape of idx_sessions_deleted.
CREATE INDEX IF NOT EXISTS idx_sessions_last_activity
    ON sessions (org_id, last_activity_at DESC, id DESC)
    WHERE deleted_at IS NULL;
