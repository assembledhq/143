ALTER TABLE pull_requests
    DROP CONSTRAINT IF EXISTS chk_pull_requests_code_review_status_comment_id,
    DROP COLUMN IF EXISTS code_review_status_comment_id;
