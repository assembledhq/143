DROP INDEX IF EXISTS idx_preview_startup_cache_base_lookup;

ALTER TABLE preview_startup_cache
    DROP COLUMN IF EXISTS base_key,
    DROP COLUMN IF EXISTS commit_sha;
