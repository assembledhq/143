DROP INDEX IF EXISTS idx_review_comments_feedback_item;
ALTER TABLE review_comments DROP COLUMN IF EXISTS source_feedback_item_id;
DROP TABLE IF EXISTS pull_request_feedback_items;
DROP TABLE IF EXISTS pull_request_feedback_batches;
ALTER TABLE pull_requests
  DROP CONSTRAINT IF EXISTS chk_pr_feedback_bot_cycles,
  DROP CONSTRAINT IF EXISTS chk_pr_feedback_monitoring,
  DROP COLUMN IF EXISTS feedback_bot_cycles_in_epoch,
  DROP COLUMN IF EXISTS feedback_bot_epoch,
  DROP COLUMN IF EXISTS feedback_monitoring;
