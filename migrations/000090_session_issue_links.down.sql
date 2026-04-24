DROP INDEX IF EXISTS idx_session_turn_issue_snapshots_org_session;
DROP TABLE IF EXISTS session_turn_issue_snapshots;

DROP INDEX IF EXISTS idx_session_issue_links_org_issue;
DROP INDEX IF EXISTS idx_session_issue_links_org_session_position;
DROP INDEX IF EXISTS idx_session_issue_links_primary;
DROP TABLE IF EXISTS session_issue_links;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_validation_policy,
    DROP CONSTRAINT IF EXISTS chk_sessions_interaction_mode,
    DROP CONSTRAINT IF EXISTS chk_sessions_origin,
    DROP COLUMN IF EXISTS validation_policy,
    DROP COLUMN IF EXISTS interaction_mode,
    DROP COLUMN IF EXISTS origin;
