-- Add archived_at column to sessions for hiding sessions from default views.
-- Unlike deleted_at (soft-delete), archived sessions are still accessible
-- but hidden from the default session list to reduce clutter.
ALTER TABLE sessions ADD COLUMN archived_at timestamptz;
ALTER TABLE sessions ADD COLUMN archived_by_user_id uuid REFERENCES users(id);

-- Partial index for listing non-archived sessions efficiently.
CREATE INDEX idx_sessions_not_archived
  ON sessions (org_id, created_at DESC, id DESC)
  WHERE archived_at IS NULL AND deleted_at IS NULL;
