CREATE TABLE linear_user_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid NOT NULL REFERENCES integrations(id),
    user_id uuid REFERENCES users(id),
    linear_workspace_key text NOT NULL DEFAULT '',
    linear_user_id text NOT NULL,
    linear_email text,
    linear_display_name text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'observed',
    linked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, linear_workspace_key, linear_user_id),
    CONSTRAINT chk_linear_user_links_source CHECK (source IN ('observed', 'email_match', 'self_linked', 'admin_linked'))
);

CREATE UNIQUE INDEX idx_linear_user_links_platform_user
    ON linear_user_links (org_id, user_id, linear_workspace_key)
    WHERE user_id IS NOT NULL;
