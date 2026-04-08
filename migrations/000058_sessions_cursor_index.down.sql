-- Reverts to the original two-column partial index.
DROP INDEX IF EXISTS idx_sessions_deleted;
CREATE INDEX idx_sessions_deleted ON sessions (org_id, created_at DESC) WHERE deleted_at IS NULL;
