DROP INDEX IF EXISTS idx_org_credentials_priority;
ALTER TABLE org_credentials DROP COLUMN priority;
