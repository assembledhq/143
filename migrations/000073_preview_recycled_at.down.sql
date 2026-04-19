DROP INDEX IF EXISTS idx_preview_instances_worker_recycled_at;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS recycled_at;
