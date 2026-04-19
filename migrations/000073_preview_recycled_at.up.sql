ALTER TABLE preview_instances
    ADD COLUMN recycled_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE preview_instances
SET recycled_at = COALESCE(updated_at, created_at);

CREATE INDEX idx_preview_instances_worker_recycled_at
    ON preview_instances (worker_node_id, recycled_at)
    WHERE status IN ('starting', 'ready', 'partially_ready', 'unhealthy');
