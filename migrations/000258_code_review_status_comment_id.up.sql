ALTER TABLE pull_requests
    ADD COLUMN code_review_status_comment_id bigint,
    ADD CONSTRAINT chk_pull_requests_code_review_status_comment_id
        CHECK (code_review_status_comment_id IS NULL OR code_review_status_comment_id > 0);
