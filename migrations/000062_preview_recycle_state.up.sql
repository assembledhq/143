ALTER TABLE preview_instances
    ADD COLUMN recycle_config JSONB,
    ADD COLUMN recycle_sandbox JSONB;
