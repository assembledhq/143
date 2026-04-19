ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS recycle_sandbox,
    DROP COLUMN IF EXISTS recycle_config;
