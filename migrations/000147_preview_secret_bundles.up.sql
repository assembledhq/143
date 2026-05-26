CREATE TABLE preview_secret_bundles (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations(id),
    name          text        NOT NULL,
    encrypted_env bytea       NOT NULL,
    created_by    uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE INDEX idx_preview_secret_bundles_org_id ON preview_secret_bundles(org_id);
