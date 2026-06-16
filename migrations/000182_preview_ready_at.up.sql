ALTER TABLE preview_instances
    ADD COLUMN ready_at timestamptz;

UPDATE preview_instances
SET ready_at = updated_at
WHERE status IN ('ready', 'partially_ready')
  AND ready_at IS NULL
  AND updated_at >= created_at;

CREATE INDEX idx_preview_instances_startup_estimate
    ON preview_instances (org_id, config_digest, ready_at DESC)
    WHERE ready_at IS NOT NULL;
