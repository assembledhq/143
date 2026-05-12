DROP INDEX IF EXISTS idx_session_threads_session_visible;

ALTER TABLE session_threads
    DROP COLUMN IF EXISTS archived_at;
