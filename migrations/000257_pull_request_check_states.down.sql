DROP INDEX IF EXISTS idx_pull_request_check_states_org_pr_head;
ALTER TABLE pull_request_health_current DROP COLUMN IF EXISTS check_state_version;
DROP TABLE IF EXISTS pull_request_check_states;
DROP SEQUENCE IF EXISTS pull_request_check_state_projection_version_seq;
