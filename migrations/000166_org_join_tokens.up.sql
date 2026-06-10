-- Multi-use, revocable org join links for zero-config CLI onboarding.
-- Deliberately a new table rather than extending invitations: invitations
-- are single-use and targeted at a person; join tokens are multi-use and
-- untargeted ("a GitHub-authenticated person may become a member of this
-- org"). They grant membership only — never API access.
CREATE TABLE org_join_tokens (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    token_hash         TEXT NOT NULL,
    token_prefix       TEXT NOT NULL,
    role               TEXT NOT NULL DEFAULT 'member',
    name               TEXT NOT NULL DEFAULT '',
    created_by_user_id UUID NOT NULL REFERENCES users(id),
    max_uses           INTEGER,
    use_count          INTEGER NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    revoked_by_user_id UUID REFERENCES users(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_org_join_tokens_role CHECK (role IN ('admin', 'member', 'builder', 'viewer'))
);

CREATE INDEX idx_org_join_tokens_org ON org_join_tokens(org_id);
-- Lookup key is the deterministic hash; token_prefix is display-only and
-- intentionally NOT unique (matches api_tokens, migration 000161).
CREATE UNIQUE INDEX idx_org_join_tokens_hash ON org_join_tokens(token_hash);
