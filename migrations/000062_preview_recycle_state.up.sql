ALTER TABLE preview_instances
    ADD COLUMN recycle_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN recycle_sandbox JSONB NOT NULL DEFAULT '{}'::jsonb;
