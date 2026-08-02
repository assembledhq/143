-- Support latest-review lookups and stable newest-first history queries for a pull request.
CREATE INDEX idx_code_review_metadata_pull_request_created
    ON code_review_session_metadata (org_id, pull_request_id, created_at DESC, id DESC);
