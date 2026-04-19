ALTER TABLE invitations ADD COLUMN github_username text;
ALTER TABLE invitations ALTER COLUMN email DROP NOT NULL;
ALTER TABLE invitations ADD CONSTRAINT invitations_email_or_github_username
    CHECK (email IS NOT NULL OR github_username IS NOT NULL);

CREATE UNIQUE INDEX idx_invitations_org_github_username_pending
    ON invitations (org_id, lower(github_username))
    WHERE status = 'pending' AND github_username IS NOT NULL;
