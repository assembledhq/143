ALTER TABLE session_executors
    ADD COLUMN container_id text;

CREATE INDEX idx_session_executors_container_id
    ON session_executors (container_id)
    WHERE container_id IS NOT NULL;
