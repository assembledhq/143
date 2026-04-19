-- Track when a long-running preview is scheduled for recycle. When this is
-- non-null and in the future, the preview is in its recycle grace period;
-- the frontend shows a warning so users can save state before the restart.
-- The recycler performs the actual recycle once the scheduled time passes.
ALTER TABLE preview_instances
    ADD COLUMN recycle_scheduled_at TIMESTAMPTZ;

-- Partial index so the recycler can efficiently find previews whose scheduled
-- recycle time has arrived without scanning all rows.
CREATE INDEX idx_preview_instances_recycle_scheduled_at
    ON preview_instances (recycle_scheduled_at)
    WHERE recycle_scheduled_at IS NOT NULL;
