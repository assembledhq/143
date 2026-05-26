ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS request_id,
    DROP COLUMN IF EXISTS current_phase;

ALTER TABLE preview_targets
    DROP COLUMN IF EXISTS request_id;
