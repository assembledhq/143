CREATE TABLE org_verified_domains (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    domain             text        NOT NULL,
    status             text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'verified')),
    verification_token text        NOT NULL UNIQUE,
    verified_at        timestamptz,
    auto_join_enabled  boolean     NOT NULL DEFAULT true,
    auto_join_role     text        NOT NULL DEFAULT 'member' CHECK (auto_join_role IN ('admin', 'member', 'builder', 'viewer')),
    created_by         uuid        NOT NULL REFERENCES users(id),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (domain)
);

CREATE INDEX idx_org_verified_domains_org_created
    ON org_verified_domains (org_id, created_at DESC);

CREATE INDEX idx_org_verified_domains_auto_join
    ON org_verified_domains (domain)
    WHERE status = 'verified' AND auto_join_enabled = true;
