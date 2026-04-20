ALTER TABLE auth_sessions DROP COLUMN IF EXISTS last_org_id;
DROP TABLE IF EXISTS organization_memberships;
