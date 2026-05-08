ALTER TABLE organization_memberships DROP CONSTRAINT IF EXISTS organization_memberships_role_check;
ALTER TABLE organization_memberships
    ADD CONSTRAINT organization_memberships_role_check CHECK (role IN ('admin', 'member', 'viewer'));

ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_role;
ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member', 'viewer')) NOT VALID;
ALTER TABLE users VALIDATE CONSTRAINT chk_users_role;
