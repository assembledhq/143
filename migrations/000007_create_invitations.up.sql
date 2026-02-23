CREATE TABLE invitations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    email       text        NOT NULL,
    role        text        NOT NULL DEFAULT 'member',
    invited_by  uuid        NOT NULL REFERENCES users(id),
    token       text        NOT NULL UNIQUE,
    status      text        NOT NULL DEFAULT 'pending',
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);

CREATE INDEX idx_invitations_org_pending ON invitations (org_id, created_at DESC)
    WHERE status = 'pending';
CREATE UNIQUE INDEX idx_invitations_org_email_pending ON invitations (org_id, email)
    WHERE status = 'pending';
