DROP INDEX IF EXISTS idx_pull_requests_session_org;
ALTER TABLE pull_requests DROP COLUMN IF EXISTS ci_status;
DROP TABLE IF EXISTS session_views;
