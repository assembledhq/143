-- Supports the organization-wide newest-first activity feed and its stable
-- (created_at, id) keyset cursor. The existing index leads with repository_id
-- and cannot serve the unfiltered organization view in this order.
CREATE INDEX idx_code_review_metadata_org_created
    ON code_review_session_metadata (org_id, created_at DESC, id DESC);
