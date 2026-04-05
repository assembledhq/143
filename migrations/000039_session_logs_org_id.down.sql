DROP INDEX IF EXISTS idx_session_logs_org_created;
ALTER TABLE session_logs DROP COLUMN IF EXISTS org_id;
