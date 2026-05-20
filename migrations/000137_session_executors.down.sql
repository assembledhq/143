DROP INDEX IF EXISTS idx_jobs_owner_kind_running;
DROP INDEX IF EXISTS idx_session_executors_job;
DROP INDEX IF EXISTS idx_session_executors_stale;
DROP INDEX IF EXISTS idx_session_executors_one_active;

DROP TABLE IF EXISTS session_executors;

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS chk_jobs_owner_kind;
ALTER TABLE jobs DROP COLUMN IF EXISTS owner_kind;
