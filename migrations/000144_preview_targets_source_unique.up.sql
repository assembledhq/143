-- Prevent duplicate preview targets for the same external event (e.g., the
-- same PR or session creating a second target row via a concurrent request).
-- The partial index covers only non-empty source_id values; manual/api targets
-- with no source_id are exempt and may have multiple rows per branch.
CREATE UNIQUE INDEX idx_preview_targets_source_unique
    ON preview_targets (org_id, repository_id, source_type, source_id)
    WHERE source_id != '';

-- Standalone index on preview_instances.preview_target_id to support
-- cascade-delete FK checks efficiently. The composite idx_preview_instances_org_target
-- is useful for org-scoped queries but requires org_id which the FK engine does
-- not have during cascade; this single-column index fills that gap.
CREATE INDEX IF NOT EXISTS idx_preview_instances_target_id
    ON preview_instances (preview_target_id)
    WHERE preview_target_id IS NOT NULL;
