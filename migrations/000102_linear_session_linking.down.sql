ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_linear_prepare_state,
    DROP COLUMN IF EXISTS linear_prepare_state,
    DROP COLUMN IF EXISTS linear_identifier_hint,
    DROP COLUMN IF EXISTS linear_state_sync_disabled,
    DROP COLUMN IF EXISTS linear_private;

DROP INDEX IF EXISTS idx_linear_team_keys_org_workspace;
DROP TABLE IF EXISTS linear_team_keys;

DROP INDEX IF EXISTS idx_session_issue_link_state_events_org_session;
DROP TABLE IF EXISTS session_issue_link_state_events;

DROP INDEX IF EXISTS idx_session_issue_link_provider_state_org_provider;
DROP TABLE IF EXISTS session_issue_link_provider_state;
