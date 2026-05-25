ALTER TABLE container_usage_events
    DROP COLUMN IF EXISTS disk_limit_mb;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS disk_limit_mb;
