ALTER TABLE session_threads
    ADD COLUMN archived_at timestamptz;

CREATE INDEX idx_session_threads_session_visible
    ON session_threads(session_id, created_at)
    WHERE archived_at IS NULL;
