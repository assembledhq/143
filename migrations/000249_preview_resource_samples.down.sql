DROP TABLE IF EXISTS preview_resource_samples;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS peak_memory_phase,
    DROP COLUMN IF EXISTS peak_memory_sampled_at,
    DROP COLUMN IF EXISTS peak_memory_bytes;
