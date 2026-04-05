DROP INDEX IF EXISTS idx_issue_events_org_created;
ALTER TABLE issue_events DROP COLUMN IF EXISTS org_id;
