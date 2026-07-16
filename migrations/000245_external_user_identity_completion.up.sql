-- Complete the provider migration started in 000216. Slack links were kept
-- dual-written during rollout; backfill every authoritative legacy mapping so
-- unified resolution is safe immediately after this migration.
INSERT INTO external_user_links (
    org_id, provider, provider_workspace_id, provider_user_id, user_id,
    source, status, confidence, external_email, external_display_name, created_at
)
SELECT
    org_id,
    'slack',
    slack_team_id,
    slack_user_id,
    user_id,
    CASE
        WHEN source = 'self_linked' THEN 'self_linked'
        WHEN source = 'admin_linked' THEN 'admin_linked'
        ELSE 'email_match'
    END,
    'active',
    CASE
        WHEN source = 'self_linked' THEN 100
        WHEN source = 'admin_linked' THEN 90
        ELSE 80
    END,
    slack_email,
    NULLIF(slack_display_name, ''),
    COALESCE(linked_at, created_at)
FROM slack_user_links
WHERE user_id IS NOT NULL
  AND source IN ('self_linked', 'admin_linked', 'email_match')
ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id)
WHERE status = 'active'
DO NOTHING;

ALTER TABLE session_attributions
    DROP CONSTRAINT chk_session_attributions_source;
ALTER TABLE session_attributions
    ADD CONSTRAINT chk_session_attributions_source CHECK (source IN ('slack', 'linear', 'external_api'));

CREATE TABLE external_user_observations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    external_email text,
    external_handle text,
    external_display_name text,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider IN ('slack', 'linear')),
    UNIQUE (org_id, provider, provider_workspace_id, provider_user_id)
);
CREATE INDEX idx_external_user_observations_recent ON external_user_observations (org_id, last_seen_at DESC);
