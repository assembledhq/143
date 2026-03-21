-- Add diff_stats and diff_history columns to sessions for per-pass diff tracking.
ALTER TABLE sessions ADD COLUMN diff_stats jsonb;
ALTER TABLE sessions ADD COLUMN diff_history jsonb DEFAULT '[]';

-- Backfill existing rows where diff_history may be NULL.
UPDATE sessions SET diff_history = '[]'::jsonb WHERE diff_history IS NULL;
