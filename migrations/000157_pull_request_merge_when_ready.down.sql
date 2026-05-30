DROP INDEX IF EXISTS idx_pull_requests_merge_when_ready;

ALTER TABLE pull_requests
    DROP CONSTRAINT IF EXISTS chk_pull_requests_merge_when_ready_state,
    DROP COLUMN IF EXISTS merge_when_ready_updated_at,
    DROP COLUMN IF EXISTS merge_when_ready_error,
    DROP COLUMN IF EXISTS merge_when_ready_health_version,
    DROP COLUMN IF EXISTS merge_when_ready_head_sha,
    DROP COLUMN IF EXISTS merge_when_ready_requested_at,
    DROP COLUMN IF EXISTS merge_when_ready_requested_by,
    DROP COLUMN IF EXISTS merge_when_ready_state;
