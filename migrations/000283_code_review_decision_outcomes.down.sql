DROP TABLE IF EXISTS code_review_human_review_observations;
DROP TABLE IF EXISTS code_review_decision_outcomes;
DROP TABLE IF EXISTS code_review_pull_request_lifecycle_observations;
DROP TABLE IF EXISTS code_review_dispute_queue_snapshots;
DROP INDEX IF EXISTS idx_code_review_disputes_rank_stale;
DROP INDEX IF EXISTS idx_code_review_disputes_contested_reasons;
DROP INDEX IF EXISTS idx_code_review_disputes_repeat_reason_window;
ALTER TABLE review_comments DROP COLUMN IF EXISTS reviewer_type;
ALTER TABLE code_review_decision_disputes DROP COLUMN IF EXISTS policy_owner_active_seconds;
