-- Add org_id to issue_events to fix the tenant isolation gap.
-- Every other table has org_id for direct tenant-scoped queries; issue_events
-- was the only exception, requiring a join through issues.

ALTER TABLE issue_events
    ADD COLUMN org_id uuid REFERENCES organizations(id);

-- Backfill org_id from the parent issue.
UPDATE issue_events ie
SET org_id = i.org_id
FROM issues i
WHERE ie.issue_id = i.id;

-- Remove orphaned rows whose parent issue no longer exists.
-- These would block the NOT NULL constraint below.
DELETE FROM issue_events WHERE org_id IS NULL;

-- Now that all rows are backfilled, make it NOT NULL.
ALTER TABLE issue_events
    ALTER COLUMN org_id SET NOT NULL;

-- Index for org-scoped event queries (e.g., "recent events across all issues in this org").
CREATE INDEX idx_issue_events_org_created ON issue_events (org_id, created_at DESC);
