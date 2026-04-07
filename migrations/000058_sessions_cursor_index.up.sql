-- Extends the partial index used by cursor-based pagination on the sessions
-- list endpoint to include id DESC as a tiebreaker, matching the new
-- ORDER BY created_at DESC, id DESC query.
DROP INDEX IF EXISTS idx_sessions_deleted;
CREATE INDEX idx_sessions_deleted ON sessions (org_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
