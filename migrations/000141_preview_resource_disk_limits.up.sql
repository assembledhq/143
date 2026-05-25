ALTER TABLE preview_instances
    ADD COLUMN disk_limit_mb INT NOT NULL DEFAULT 10240;

ALTER TABLE container_usage_events
    ADD COLUMN disk_limit_mb INT NOT NULL DEFAULT 10240;
