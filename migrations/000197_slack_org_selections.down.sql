DROP TABLE IF EXISTS slack_org_selections;

CREATE UNIQUE INDEX IF NOT EXISTS idx_slack_installations_team_app_active_unique
    ON slack_installations (team_id, api_app_id)
    WHERE status = 'active';
