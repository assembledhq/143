DROP INDEX IF EXISTS idx_preview_instances_startup_estimate;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS ready_at;
