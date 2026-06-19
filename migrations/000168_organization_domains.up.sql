-- Verified email domains for auto-join ("domain capture").
--
-- An admin adds their company domain, proves ownership by publishing a DNS
-- TXT record, and from then on new OAuth signups whose provider-verified
-- email matches the domain are placed directly into the org instead of
-- being stranded in a fresh single-user org.
CREATE TABLE organization_domains (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    -- Always stored lowercase; handlers normalize before insert.
    domain             text        NOT NULL,
    -- The opaque value the admin publishes as a DNS TXT record. Not a
    -- secret (it is world-readable in DNS once published) but unguessable,
    -- so a verified row proves control of the zone at verification time.
    verification_token text        NOT NULL,
    status             text        NOT NULL DEFAULT 'pending'
        CONSTRAINT chk_organization_domains_status CHECK (status IN ('pending', 'verified')),
    auto_join_enabled  boolean     NOT NULL DEFAULT true,
    created_by         uuid        REFERENCES users(id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    verified_at        timestamptz,
    -- Most recent DNS verification attempt (successful or not), so admins
    -- can see staleness and the daily re-check sweep has a watermark.
    last_checked_at    timestamptz,
    -- Consecutive failed daily re-checks of a VERIFIED domain's TXT record.
    -- At 3 the sweep disables auto_join_enabled (expired/transferred-domain
    -- hygiene) while keeping status='verified' so no other org can claim
    -- the domain out from under its owner. Reset on any successful check.
    failed_checks      integer     NOT NULL DEFAULT 0
);

-- One row per (org, domain): re-adding the same domain edits the existing
-- row rather than minting a second token.
CREATE UNIQUE INDEX idx_org_domains_org_domain ON organization_domains (org_id, domain);

-- A domain can be VERIFIED by at most one org globally (the Google/Figma/
-- OpenAI model: one workspace owns a domain). Pending claims may coexist —
-- whoever completes DNS verification first wins; the loser's verify attempt
-- fails on this index.
CREATE UNIQUE INDEX idx_org_domains_verified_domain ON organization_domains (domain) WHERE status = 'verified';

-- Provider-asserted email verification watermark. Set when an OAuth
-- provider attests the address (Google userinfo email_verified, GitHub
-- /user/emails verified flag); NULL for password signups, which have no
-- verification flow yet. Domain auto-join is gated on this being set so a
-- password signup claiming ceo@victim.com cannot walk into victim.com's org.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;

-- Backfill: google_id is only ever set from Google OAuth, and Google
-- userinfo emails for signed-in accounts are provider-verified. GitHub
-- users are NOT backfilled — the profile email's verified flag wasn't
-- recorded historically — they get stamped on their next login.
UPDATE users SET email_verified_at = now() WHERE google_id IS NOT NULL;

-- Email-verification tokens for password-signup users (OAuth users are
-- attested by their provider and never need one). Single-use, short-lived;
-- email is snapshotted so a token can't verify an address the account no
-- longer holds.
CREATE TABLE email_verification_tokens ( -- lint:no-org-id reason="pre-membership user-identity table; tokens exist before (and independent of) any org affiliation"
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email       text        NOT NULL,
    token       text        NOT NULL UNIQUE,
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_email_verification_tokens_user ON email_verification_tokens (user_id);
