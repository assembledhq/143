ALTER TABLE slack_session_links
    ADD COLUMN latest_progress_kind text;

ALTER TABLE slack_channel_settings
    ALTER COLUMN response_visibility DROP NOT NULL,
    ALTER COLUMN allowed_actions DROP NOT NULL,
    ALTER COLUMN notification_subscriptions DROP NOT NULL,
    ADD COLUMN routing_mode text,
    ADD COLUMN notification_preset text;

ALTER TABLE slack_channel_settings
    ADD CONSTRAINT chk_slack_channel_settings_routing_mode
        CHECK (routing_mode IS NULL OR routing_mode IN ('auto', 'answer_only', 'start_work')),
    ADD CONSTRAINT chk_slack_channel_settings_response_visibility
        CHECK (response_visibility IS NULL OR response_visibility IN ('thread', 'dm')),
    ADD CONSTRAINT chk_slack_channel_settings_notification_preset
        CHECK (notification_preset IS NULL OR notification_preset IN ('quiet', 'balanced', 'verbose', 'custom')),
    ADD CONSTRAINT chk_slack_channel_settings_allowed_actions
        CHECK (
            allowed_actions IS NULL OR
            allowed_actions <@ ARRAY['session','preview','pr_request','human_input']::text[]
        );
