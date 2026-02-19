CREATE TABLE org_credentials (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    provider         text        NOT NULL,
    config           bytea       NOT NULL,
    status           text        NOT NULL DEFAULT 'active',
    last_verified_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, provider)
);

CREATE INDEX idx_org_credentials_org_id ON org_credentials(org_id);
