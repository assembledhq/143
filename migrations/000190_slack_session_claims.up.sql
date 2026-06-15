CREATE TABLE slack_session_claims (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    slack_session_link_id uuid NOT NULL REFERENCES slack_session_links(id) ON DELETE CASCADE,
    claimed_by_user_id uuid NOT NULL REFERENCES users(id),
    claimed_by_slack_user_id text NOT NULL,
    claimed_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, slack_session_link_id)
);

CREATE INDEX idx_slack_session_claims_org_user
    ON slack_session_claims (org_id, claimed_by_user_id, claimed_at DESC);
