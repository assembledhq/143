CREATE TABLE slack_bot_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    default_repository_id uuid REFERENCES repositories(id),
    default_branch text,
    routing_mode text NOT NULL DEFAULT 'auto',
    response_visibility text NOT NULL DEFAULT 'thread',
    allowed_actions text[] NOT NULL DEFAULT '{session,preview}',
    notification_preset text NOT NULL DEFAULT 'balanced',
    notification_subscriptions jsonb NOT NULL DEFAULT '{}'::jsonb,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_installation_id),
    CONSTRAINT chk_slack_bot_settings_routing_mode CHECK (routing_mode IN ('auto', 'answer_only', 'start_work')),
    CONSTRAINT chk_slack_bot_settings_response_visibility CHECK (response_visibility IN ('thread', 'dm')),
    CONSTRAINT chk_slack_bot_settings_notification_preset CHECK (notification_preset IN ('quiet', 'balanced', 'verbose', 'custom')),
    CONSTRAINT chk_slack_bot_settings_allowed_actions CHECK (
        allowed_actions <@ ARRAY['session','preview','pr_request','human_input']::text[]
    )
);

CREATE INDEX idx_slack_bot_settings_org_active
    ON slack_bot_settings (org_id, updated_at DESC)
    WHERE active = true;
