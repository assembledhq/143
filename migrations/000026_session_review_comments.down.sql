DROP TABLE IF EXISTS session_review_comments;
ALTER TABLE sessions DROP COLUMN IF EXISTS diff_stats;
ALTER TABLE sessions DROP COLUMN IF EXISTS diff_history;
