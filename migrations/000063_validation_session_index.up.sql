-- Replace the single-column index on session_id (originally agent_run_id) with
-- a composite index that covers the GetBySessionID query:
--   WHERE session_id = ? AND org_id = ?
--
-- The old idx_validations_run (session_id) is subsumed by this index.
-- NOTE: For production, create this index CONCURRENTLY before running the migration.

DROP INDEX IF EXISTS idx_validations_run;
CREATE INDEX idx_validations_session_org ON validations (session_id, org_id);
