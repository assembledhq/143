DROP INDEX IF EXISTS idx_pull_request_repair_runs_org_pr;
DROP INDEX IF EXISTS idx_pull_request_repair_runs_active;
DROP INDEX IF EXISTS idx_pull_request_health_snapshots_org_pr;
DROP INDEX IF EXISTS idx_pull_request_health_current_org;
DROP INDEX IF EXISTS idx_pull_requests_health_repo;
DROP INDEX IF EXISTS idx_pull_requests_health_actions;
DROP INDEX IF EXISTS idx_pull_requests_health_tests;
DROP INDEX IF EXISTS idx_pull_requests_health_conflicts;

DROP TABLE IF EXISTS pull_request_repair_runs;
DROP TABLE IF EXISTS pull_request_health_current;
DROP TABLE IF EXISTS pull_request_health_snapshots;

ALTER TABLE pull_requests
    DROP COLUMN IF EXISTS health_version,
    DROP COLUMN IF EXISTS github_state_synced_at,
    DROP COLUMN IF EXISTS needs_agent_action,
    DROP COLUMN IF EXISTS failing_test_count,
    DROP COLUMN IF EXISTS has_conflicts,
    DROP COLUMN IF EXISTS merge_state,
    DROP COLUMN IF EXISTS base_sha,
    DROP COLUMN IF EXISTS head_sha;
