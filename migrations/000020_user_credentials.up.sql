CREATE TABLE user_credentials (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    provider         text        NOT NULL,
    config           bytea       NOT NULL,
    is_team_default  boolean     NOT NULL DEFAULT false,
    status           text        NOT NULL DEFAULT 'active',
    last_verified_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id, provider)
);

CREATE INDEX idx_user_credentials_org_id ON user_credentials(org_id);
CREATE INDEX idx_user_credentials_user_id ON user_credentials(user_id);
