ALTER TABLE sessions
    ADD COLUMN worker_node_id TEXT;

CREATE INDEX idx_sessions_worker_node_status
    ON sessions (worker_node_id, status)
    WHERE worker_node_id IS NOT NULL;
