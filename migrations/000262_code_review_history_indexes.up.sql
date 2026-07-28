-- Support newest-first review history pagination across an organization.
CREATE INDEX idx_code_review_metadata_org_created
    ON code_review_session_metadata (org_id, created_at DESC, id DESC);

-- Support latest-review lookups and retry-eligibility checks per pull request.
CREATE INDEX idx_code_review_metadata_pull_request_created
    ON code_review_session_metadata (org_id, pull_request_id, created_at DESC, id DESC);
