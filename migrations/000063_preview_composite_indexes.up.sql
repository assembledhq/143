-- Composite indexes for preview instance query patterns used in cap enforcement and cleanup.

-- Used by CountActivePreviewsByOrg (called on every preview start).
CREATE INDEX IF NOT EXISTS idx_preview_instances_org_status
  ON preview_instances (org_id, status);

-- Used by CountActivePreviewsByUser (called on every preview start).
CREATE INDEX IF NOT EXISTS idx_preview_instances_org_user_status
  ON preview_instances (org_id, user_id, status);

-- Used by ListExpiredPreviews (called by TTL cleanup worker).
CREATE INDEX IF NOT EXISTS idx_preview_instances_status_expires
  ON preview_instances (status, expires_at)
  WHERE expires_at IS NOT NULL;

-- Used by ListIdlePreviews (called by idle cleanup worker).
CREATE INDEX IF NOT EXISTS idx_preview_instances_status_last_accessed
  ON preview_instances (status, last_accessed_at);
