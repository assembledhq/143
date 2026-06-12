DROP INDEX IF EXISTS idx_preview_instances_org_terminal;

ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS preview_instances_stopped_reason_check,
    DROP COLUMN IF EXISTS stopped_reason;

ALTER TABLE preview_targets
    DROP COLUMN IF EXISTS last_snapshot_key;

DROP INDEX IF EXISTS idx_repository_preview_policies_repo;
DROP TABLE IF EXISTS repository_preview_policies;
