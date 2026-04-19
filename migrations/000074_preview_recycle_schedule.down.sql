DROP INDEX IF EXISTS idx_preview_instances_recycle_scheduled_at;
ALTER TABLE preview_instances DROP COLUMN IF EXISTS recycle_scheduled_at;
