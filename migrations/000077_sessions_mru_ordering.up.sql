-- Switch the sessions list to Most-Recently-Updated ordering by making
-- last_activity_at the authoritative "last touched" timestamp for every
-- session and backing the new ORDER BY with a matching partial index.
--
-- DEPLOY ORDER: this migration MUST run before any backend that issues
-- `ORDER BY last_activity_at` ships to that database. The new partial index
-- is what keeps the list query off a table scan; a backend that runs the
-- new query against a DB that hasn't applied this migration will still
-- function (the NULL-producing COALESCE backfill is not strictly required
-- for the ORDER BY), but will pay a seq-scan cost on every page load.
-- Roll out: apply migration -> verify index is present -> deploy backend.
--
-- LARGE-TABLE NOTE: on deployments where `sessions` has grown past ~1M rows,
-- do not rely on this migration's inline steps to finish quickly. The inline
-- `SET NOT NULL` falls back to a full-table ACCESS EXCLUSIVE scan when the
-- CHECK NOT VALID / VALIDATE runbook hasn't been completed. Run the full
-- runbook below out-of-band first; the migration then finishes in
-- milliseconds.
--
-- PRODUCTION RUNBOOK (do these out-of-band BEFORE running this migration so
-- the migrate step is fast and lock-light):
--
--   1) Backfill (idempotent — same statement runs again below as a safety net):
--        UPDATE sessions
--        SET last_activity_at = COALESCE(last_activity_at, completed_at, started_at, created_at)
--        WHERE last_activity_at IS NULL;
--
--   2) Build the new index without blocking writes:
--        CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sessions_last_activity
--          ON sessions (org_id, last_activity_at DESC, id DESC)
--          WHERE deleted_at IS NULL;
--
--   3) Validate the NOT NULL invariant without blocking writes:
--        ALTER TABLE sessions
--          ADD CONSTRAINT sessions_last_activity_at_not_null
--          CHECK (last_activity_at IS NOT NULL) NOT VALID;
--        ALTER TABLE sessions
--          VALIDATE CONSTRAINT sessions_last_activity_at_not_null;
--
-- The migration below is idempotent and safe to re-run; if step 3 has already
-- been done, Postgres 12+ uses the validated CHECK to skip the table scan
-- when SET NOT NULL runs, so the only ACCESS EXCLUSIVE lock is a brief
-- catalog update. If the runbook hasn't been done, the migration still works
-- but will take an ACCESS EXCLUSIVE lock for a full table scan.

-- Backfill (safety net — no-op if the runbook backfill already ran).
UPDATE sessions
SET last_activity_at = COALESCE(last_activity_at, completed_at, started_at, created_at)
WHERE last_activity_at IS NULL;

-- Default for new inserts. Adding a default is a catalog-only change in PG11+
-- (no table rewrite).
ALTER TABLE sessions
    ALTER COLUMN last_activity_at SET DEFAULT now();

-- Promote the validated CHECK to NOT NULL (PG12+ skips the rescan), or fall
-- back to a full scan if the CHECK isn't there.
ALTER TABLE sessions
    ALTER COLUMN last_activity_at SET NOT NULL;

-- Drop the helper CHECK if it exists from the runbook — NOT NULL now covers it.
ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_last_activity_at_not_null;

-- Build the partial index supporting `ORDER BY last_activity_at DESC, id DESC`
-- with the `org_id` prefix and `deleted_at IS NULL` filter used by
-- SessionStore.ListByOrg. IF NOT EXISTS is intentional so the runbook's
-- CONCURRENT build is reused if present.
CREATE INDEX IF NOT EXISTS idx_sessions_last_activity
    ON sessions (org_id, last_activity_at DESC, id DESC)
    WHERE deleted_at IS NULL;

-- Drop the old MRU index — its sole consumer was ListByOrg's previous
-- `ORDER BY created_at DESC, id DESC`, which now uses idx_sessions_last_activity.
-- DROP INDEX takes a brief ACCESS EXCLUSIVE lock; in production prefer running
-- `DROP INDEX CONCURRENTLY IF EXISTS idx_sessions_deleted;` out-of-band before
-- this migration. IF EXISTS keeps the migration idempotent either way.
DROP INDEX IF EXISTS idx_sessions_deleted;
