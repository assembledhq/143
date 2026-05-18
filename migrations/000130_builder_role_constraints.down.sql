ALTER TABLE users
    DROP CONSTRAINT IF EXISTS chk_users_role;

UPDATE users
SET role = 'member'
WHERE role = 'builder';

ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member', 'viewer'));

ALTER TABLE organization_memberships
    DROP CONSTRAINT IF EXISTS organization_memberships_role_check;

UPDATE organization_memberships
SET role = 'member'
WHERE role = 'builder';

ALTER TABLE organization_memberships
    ADD CONSTRAINT organization_memberships_role_check CHECK (role IN ('admin', 'member', 'viewer'));
