ALTER TABLE slack_channel_settings
    DROP CONSTRAINT IF EXISTS chk_slack_channel_settings_allowed_actions,
    DROP CONSTRAINT IF EXISTS chk_slack_channel_settings_notification_preset,
    DROP CONSTRAINT IF EXISTS chk_slack_channel_settings_response_visibility,
    DROP CONSTRAINT IF EXISTS chk_slack_channel_settings_routing_mode,
    DROP COLUMN IF EXISTS notification_preset,
    DROP COLUMN IF EXISTS routing_mode;

UPDATE slack_channel_settings
SET response_visibility = COALESCE(response_visibility, 'thread'),
    allowed_actions = COALESCE(allowed_actions, ARRAY['session','preview']::text[]),
    notification_subscriptions = COALESCE(notification_subscriptions, '{}'::jsonb);

ALTER TABLE slack_channel_settings
    ALTER COLUMN response_visibility SET NOT NULL,
    ALTER COLUMN allowed_actions SET NOT NULL,
    ALTER COLUMN notification_subscriptions SET NOT NULL;

ALTER TABLE slack_session_links
    DROP COLUMN IF EXISTS latest_progress_kind;
