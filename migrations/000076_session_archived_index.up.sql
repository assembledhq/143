-- Partial index for counting/listing archived sessions efficiently.
-- The existing idx_sessions_not_archived covers archived_at IS NULL; this is
-- its mirror for the archived bucket used by /api/v1/sessions/counts, so a
-- bounded count scan terminates under LIMIT even on orgs with very few
-- archived rows.
CREATE INDEX idx_sessions_archived
  ON sessions (org_id, created_at DESC, id DESC)
  WHERE archived_at IS NOT NULL AND deleted_at IS NULL;
