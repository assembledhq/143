-- Add the Builder org role. Builder sits between member and viewer and can be
-- assigned in team settings. Any future builder-specific product guardrails
-- should be enforced in the application layer rather than the schema.

ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_role;
ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member', 'builder', 'viewer')) NOT VALID;
ALTER TABLE users VALIDATE CONSTRAINT chk_users_role;

ALTER TABLE organization_memberships DROP CONSTRAINT IF EXISTS organization_memberships_role_check;
ALTER TABLE organization_memberships
    ADD CONSTRAINT organization_memberships_role_check CHECK (role IN ('admin', 'member', 'builder', 'viewer'));
