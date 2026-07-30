CREATE INDEX idx_code_review_metadata_org_created
    ON code_review_session_metadata (org_id, created_at DESC);
