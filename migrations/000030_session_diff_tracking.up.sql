-- Add diff_stats and diff_history columns to sessions for per-pass diff tracking.
-- Note: diff_history defaults to '[]' for new rows. Existing rows will have NULL,
-- which is handled by COALESCE in diffHistoryAppendSQL — no backfill needed.
ALTER TABLE sessions ADD COLUMN diff_stats jsonb;
ALTER TABLE sessions ADD COLUMN diff_history jsonb DEFAULT '[]';
