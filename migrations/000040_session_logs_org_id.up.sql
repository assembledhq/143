-- Add org_id to session_logs to fix the tenant isolation gap.
-- session_logs was the remaining high-volume table without direct org_id,
-- requiring an expensive join through sessions for tenant-scoped queries.
-- This follows the same fix pattern as migration 35 (issue_events).
--
-- DEPENDS ON: Migration 000037 (partitioning_prep) which recreates session_logs
-- as a partitioned table. This migration adds a column to that partitioned table.

ALTER TABLE session_logs
    ADD COLUMN org_id uuid REFERENCES organizations(id);

-- Backfill org_id from the parent session.
UPDATE session_logs sl
SET org_id = s.org_id
FROM sessions s
WHERE sl.session_id = s.id;

-- Remove orphaned rows whose parent session no longer exists.
-- These would block the NOT NULL constraint below.
DELETE FROM session_logs WHERE org_id IS NULL;

-- Now that all rows are backfilled, make it NOT NULL.
ALTER TABLE session_logs
    ALTER COLUMN org_id SET NOT NULL;

-- Index for org-scoped log queries without joining through sessions.
CREATE INDEX idx_session_logs_org_created ON session_logs (org_id, timestamp DESC);
