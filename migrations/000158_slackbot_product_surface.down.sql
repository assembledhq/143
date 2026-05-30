DROP TABLE IF EXISTS slack_outbound_messages;
DROP TABLE IF EXISTS slack_inbound_events;
DROP TABLE IF EXISTS slack_session_links;
DROP TABLE IF EXISTS slack_channel_settings;
DROP INDEX IF EXISTS idx_slack_user_links_platform_user;
DROP TABLE IF EXISTS slack_user_links;
DROP INDEX IF EXISTS idx_slack_installations_team_app_active_unique;
DROP TABLE IF EXISTS slack_installations;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision'));
