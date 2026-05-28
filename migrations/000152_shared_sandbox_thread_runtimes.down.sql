BEGIN;

DROP INDEX IF EXISTS idx_session_executors_one_active_thread;
DROP INDEX IF EXISTS idx_session_executors_one_active_unthreaded;

CREATE UNIQUE INDEX idx_session_executors_one_active
    ON session_executors (org_id, session_id)
    WHERE status IN ('starting', 'running', 'draining');

DROP TABLE IF EXISTS session_sandbox_holders;
DROP TABLE IF EXISTS thread_inbox_entries;
DROP TABLE IF EXISTS thread_runtimes;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS workspace_generation;

COMMIT;
