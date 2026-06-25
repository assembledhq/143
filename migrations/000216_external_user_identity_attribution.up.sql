CREATE TABLE external_user_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    source text NOT NULL,
    status text NOT NULL DEFAULT 'active',
    confidence integer NOT NULL,
    external_email text,
    external_handle text,
    external_display_name text,
    linked_by_user_id uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    CHECK (provider IN ('slack', 'linear')),
    CHECK (source IN ('self_linked', 'admin_linked', 'email_match', 'directory')),
    CHECK (status IN ('active', 'revoked')),
    CHECK (confidence BETWEEN 0 AND 100)
);

CREATE UNIQUE INDEX idx_external_user_links_active_external
    ON external_user_links (org_id, provider, provider_workspace_id, provider_user_id)
    WHERE status = 'active';

CREATE INDEX idx_external_user_links_user
    ON external_user_links (org_id, user_id)
    WHERE status = 'active';

CREATE TABLE external_user_link_suggestions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    suggested_user_id uuid NOT NULL REFERENCES users(id),
    reason text NOT NULL,
    confidence integer NOT NULL,
    external_email text,
    external_handle text,
    external_display_name text,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    dismissed_at timestamptz,
    CHECK (provider IN ('slack', 'linear')),
    CHECK (confidence BETWEEN 0 AND 100)
);

CREATE UNIQUE INDEX idx_external_user_link_suggestions_open
    ON external_user_link_suggestions (
        org_id, provider, provider_workspace_id, provider_user_id, suggested_user_id
    )
    WHERE dismissed_at IS NULL;

CREATE TABLE external_user_link_claims (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    provider text NOT NULL,
    provider_workspace_id text NOT NULL,
    provider_user_id text NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    source_context jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at timestamptz NOT NULL,
    claimed_by_user_id uuid REFERENCES users(id),
    claimed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (provider IN ('slack', 'linear'))
);

CREATE INDEX idx_external_user_link_claims_open
    ON external_user_link_claims (org_id, provider, provider_workspace_id, provider_user_id, expires_at)
    WHERE claimed_at IS NULL;

INSERT INTO external_user_links (
    org_id, provider, provider_workspace_id, provider_user_id, user_id,
    source, status, confidence, external_email, external_display_name, created_at
)
SELECT
    org_id,
    'linear',
    linear_workspace_id,
    linear_user_id,
    user_id,
    CASE
        WHEN source IN ('self_linked', 'admin_linked') THEN source
        ELSE 'email_match'
    END,
    'active',
    CASE
        WHEN source = 'self_linked' THEN 100
        WHEN source = 'admin_linked' THEN 90
        ELSE 80
    END,
    linear_email,
    NULLIF(linear_display_name, ''),
    COALESCE(linked_at, created_at)
FROM linear_user_links
WHERE user_id IS NOT NULL
ON CONFLICT (org_id, provider, provider_workspace_id, provider_user_id)
WHERE status = 'active'
DO NOTHING;
