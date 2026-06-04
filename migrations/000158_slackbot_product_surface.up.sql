ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision', 'slack'));

CREATE TABLE slack_installations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid NOT NULL REFERENCES integrations(id),
    team_id text NOT NULL,
    team_name text NOT NULL DEFAULT '',
    enterprise_id text,
    api_app_id text NOT NULL DEFAULT '',
    bot_user_id text NOT NULL DEFAULT '',
    bot_id text NOT NULL DEFAULT '',
    scope text[] NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT 'active',
    installed_by_user_id uuid REFERENCES users(id),
    installed_at timestamptz NOT NULL DEFAULT now(),
    last_event_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, team_id, api_app_id),
    CONSTRAINT chk_slack_installations_status CHECK (status IN ('active', 'disconnected'))
);

CREATE INDEX idx_slack_installations_team_app_active
    ON slack_installations (team_id, api_app_id)
    WHERE status = 'active';

-- One active installation per Slack workspace globally: prevents GetActiveByTeamApp
-- from returning installations belonging to different orgs for the same workspace.
CREATE UNIQUE INDEX idx_slack_installations_team_app_active_unique
    ON slack_installations (team_id, api_app_id)
    WHERE status = 'active';

CREATE TABLE slack_user_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    user_id uuid REFERENCES users(id),
    slack_team_id text NOT NULL,
    slack_user_id text NOT NULL,
    slack_email text,
    slack_display_name text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'observed',
    linked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_user_id),
    CONSTRAINT chk_slack_user_links_source CHECK (source IN ('observed', 'email_match', 'self_linked', 'admin_linked'))
);

-- Enforce one Slack user mapping per platform user per team, but only when a
-- platform user has actually been resolved (user_id nullable for unmatched users).
CREATE UNIQUE INDEX idx_slack_user_links_platform_user
    ON slack_user_links (org_id, user_id, slack_team_id)
    WHERE user_id IS NOT NULL;

CREATE TABLE slack_channel_settings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_channel_name text NOT NULL DEFAULT '',
    channel_type text NOT NULL DEFAULT 'channel',
    default_repository_id uuid REFERENCES repositories(id),
    default_branch text,
    response_visibility text NOT NULL DEFAULT 'thread',
    allowed_actions text[] NOT NULL DEFAULT '{session,preview}',
    notification_subscriptions jsonb NOT NULL DEFAULT '{}'::jsonb,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id)
);

CREATE TABLE slack_session_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_thread_ts text NOT NULL,
    slack_root_ts text NOT NULL DEFAULT '',
    slack_message_permalink text NOT NULL DEFAULT '',
    slack_user_id text NOT NULL DEFAULT '',
    mapped_user_id uuid REFERENCES users(id),
    team_session boolean NOT NULL DEFAULT false,
    latest_status_message_ts text,
    final_message_ts text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id, slack_thread_ts)
);

CREATE INDEX idx_slack_session_links_session
    ON slack_session_links (org_id, session_id);

CREATE TABLE slack_inbound_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id),
    slack_event_id text,
    slack_team_id text NOT NULL,
    event_type text NOT NULL,
    channel_id text,
    user_id text,
    event_ts text,
    payload jsonb NOT NULL,
    status text NOT NULL DEFAULT 'received',
    job_id uuid,
    error text,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    CONSTRAINT chk_slack_inbound_events_status CHECK (status IN ('received', 'enqueued', 'processed', 'failed'))
);

CREATE INDEX idx_slack_inbound_events_org_received
    ON slack_inbound_events (org_id, received_at DESC);

-- Partial unique index so NULL slack_event_id values (synthetic IDs) do not
-- defeat deduplication for real Slack event IDs.
CREATE UNIQUE INDEX idx_slack_inbound_events_dedup
    ON slack_inbound_events (org_id, slack_event_id)
    WHERE slack_event_id IS NOT NULL;

CREATE TABLE slack_outbound_messages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_session_link_id uuid REFERENCES slack_session_links(id) ON DELETE CASCADE,
    notification_id uuid,
    slack_team_id text NOT NULL,
    slack_channel_id text NOT NULL,
    slack_message_ts text NOT NULL,
    message_kind text NOT NULL,
    status text NOT NULL DEFAULT 'posted',
    last_payload_hash text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_team_id, slack_channel_id, slack_message_ts)
);

CREATE INDEX idx_slack_outbound_messages_session_link
    ON slack_outbound_messages (org_id, slack_session_link_id)
    WHERE slack_session_link_id IS NOT NULL;
