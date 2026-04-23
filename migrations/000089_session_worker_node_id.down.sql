DROP INDEX IF EXISTS idx_sessions_worker_node_status;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS worker_node_id;
