-- Prevent duplicate preview targets for the same external event (e.g., the
-- same PR or session creating a second target row via a concurrent request).
-- The partial index covers only non-empty source_id values; manual/api targets
-- with no source_id are exempt and may have multiple rows per branch.
CREATE UNIQUE INDEX idx_preview_targets_source_unique
    ON preview_targets (org_id, repository_id, source_type, source_id)
    WHERE source_id != '';

-- Covering index for the GetLatestPreviewTargetForBranch query, which is the
-- hot path for PR-state and stable-link resolution. Without this the planner
-- falls back to idx_preview_targets_identity which includes commit_sha and
-- requires a separate sort pass for the ORDER BY created_at DESC.
CREATE INDEX IF NOT EXISTS idx_preview_targets_branch_state
    ON preview_targets (org_id, repository_id, branch, preview_config_name, created_at DESC);

-- Standalone index on preview_instances.preview_target_id to support
-- cascade-delete FK checks efficiently. The composite idx_preview_instances_org_target
-- is useful for org-scoped queries but requires org_id which the FK engine does
-- not have during cascade; this single-column index fills that gap.
--
-- Must be a full (non-partial) index: the PostgreSQL FK constraint engine does
-- not apply a WHERE clause when scanning for referencing rows, so a partial
-- index (WHERE preview_target_id IS NOT NULL) is unusable and forces a
-- sequential scan of the whole table on every cascade-delete.
CREATE INDEX IF NOT EXISTS idx_preview_instances_target_id
    ON preview_instances (preview_target_id);
