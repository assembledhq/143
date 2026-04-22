DROP INDEX IF EXISTS idx_jobs_running_by_node;
DROP INDEX IF EXISTS idx_jobs_reclaimable;

ALTER TABLE jobs
    DROP COLUMN IF EXISTS run_owner_id,
    DROP COLUMN IF EXISTS lock_token,
    DROP COLUMN IF EXISTS lease_expires_at;
