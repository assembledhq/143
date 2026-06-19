DROP INDEX IF EXISTS idx_pull_request_repair_runs_active_head;

ALTER TABLE pull_request_repair_runs
    DROP COLUMN IF EXISTS base_sha,
    DROP COLUMN IF EXISTS head_sha;
