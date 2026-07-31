ALTER TABLE code_review_session_metadata
    ADD COLUMN additions integer,
    ADD COLUMN deletions integer,
    ADD COLUMN risk_reason_details jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_code_review_session_metadata_additions
        CHECK (additions IS NULL OR additions >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_deletions
        CHECK (deletions IS NULL OR deletions >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_change_breakdown
        CHECK (
            (additions IS NULL AND deletions IS NULL)
            OR (additions IS NOT NULL AND deletions IS NOT NULL)
        );

CREATE INDEX idx_code_review_metadata_analytics
    ON code_review_session_metadata (org_id, created_at DESC)
    WHERE status = 'completed';
