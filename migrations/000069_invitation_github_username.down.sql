DROP INDEX IF EXISTS idx_invitations_org_github_username_pending;
ALTER TABLE invitations DROP CONSTRAINT IF EXISTS invitations_email_or_github_username;
UPDATE invitations SET email = '' WHERE email IS NULL;
ALTER TABLE invitations ALTER COLUMN email SET NOT NULL;
ALTER TABLE invitations DROP COLUMN github_username;
