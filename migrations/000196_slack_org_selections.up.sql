DROP INDEX IF EXISTS idx_slack_installations_team_app_active_unique;

CREATE TABLE slack_org_selections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_installation_id uuid NOT NULL REFERENCES slack_installations(id) ON DELETE CASCADE,
    slack_team_id text NOT NULL,
    api_app_id text NOT NULL DEFAULT '',
    slack_user_id text NOT NULL,
    selected_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (slack_team_id, api_app_id, slack_user_id)
);

CREATE INDEX idx_slack_org_selections_org_user
    ON slack_org_selections (org_id, slack_user_id);

CREATE INDEX idx_slack_org_selections_installation
    ON slack_org_selections (org_id, slack_installation_id);
