DROP INDEX IF EXISTS idx_sessions_orphaned_containers;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS preview_holding_container;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS turn_holding_container;
