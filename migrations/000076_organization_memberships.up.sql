-- Multi-organization membership support.
--
-- Introduces a join table between users and organizations so a single user
-- identity can belong to multiple orgs. `users.org_id` and `users.role` are
-- kept during the compatibility window — derived values during transition.
-- `auth_sessions.last_org_id` is the bootstrap hint used when a new tab opens
-- and before the client has echoed back an X-Active-Org-ID header.
--
-- Only this migration is required to ship the membership infrastructure.
-- Dropping the legacy columns happens in a later, gated migration.

CREATE TABLE organization_memberships (
    user_id    uuid        NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    org_id     uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_id)
);

CREATE INDEX idx_memberships_user ON organization_memberships (user_id);
CREATE INDEX idx_memberships_org  ON organization_memberships (org_id);

-- Backfill one membership per existing user, preserving role.
INSERT INTO organization_memberships (user_id, org_id, role, created_at)
SELECT id, org_id, role, created_at
FROM users
ON CONFLICT (user_id, org_id) DO NOTHING;

-- `last_org_id` is a per-session hint. Nullable so sign-in still works for
-- zero-membership users (empty state) and if the session's last org is
-- deleted. The old `org_id` column stays in place for the compatibility
-- window; middleware reads from memberships but falls back to the session's
-- `last_org_id` when the X-Active-Org-ID header is not present.
ALTER TABLE auth_sessions ADD COLUMN last_org_id uuid REFERENCES organizations(id) ON DELETE SET NULL;

-- Backfill last_org_id from the session's existing org_id so middleware can
-- start using it immediately without behavioral change.
UPDATE auth_sessions SET last_org_id = org_id WHERE last_org_id IS NULL;
