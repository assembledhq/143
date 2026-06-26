DROP INDEX IF EXISTS idx_session_executors_container_id;

ALTER TABLE session_executors
    DROP COLUMN IF EXISTS container_id;
