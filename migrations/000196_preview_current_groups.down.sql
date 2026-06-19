DROP INDEX IF EXISTS idx_preview_targets_group_created;

ALTER TABLE preview_targets
    DROP COLUMN IF EXISTS preview_group_id;

DROP INDEX IF EXISTS idx_preview_groups_org_activity;
DROP INDEX IF EXISTS idx_preview_groups_org_status_activity;
DROP INDEX IF EXISTS idx_preview_groups_identity;
DROP TABLE IF EXISTS preview_groups;
