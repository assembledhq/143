DROP INDEX IF EXISTS idx_code_review_metadata_analytics;

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT IF EXISTS chk_code_review_session_metadata_change_breakdown,
    DROP CONSTRAINT IF EXISTS chk_code_review_session_metadata_deletions,
    DROP CONSTRAINT IF EXISTS chk_code_review_session_metadata_additions,
    DROP COLUMN IF EXISTS risk_reason_details,
    DROP COLUMN IF EXISTS deletions,
    DROP COLUMN IF EXISTS additions;
