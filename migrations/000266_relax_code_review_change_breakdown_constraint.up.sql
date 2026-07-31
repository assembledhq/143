-- Migration 000263 originally required lines_changed to equal additions plus
-- deletions. The size-metrics cleanup later stopped writing lines_changed and
-- changed 000263 in place, but databases that had already applied the original
-- migration retained the stricter constraint. Replace only the constraint so
-- rolling workers can write the supported additions/deletions pair without
-- deleting legacy size data.
ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT IF EXISTS chk_code_review_session_metadata_change_breakdown;

ALTER TABLE code_review_session_metadata
    ADD CONSTRAINT chk_code_review_session_metadata_change_breakdown
        CHECK (
            (additions IS NULL AND deletions IS NULL)
            OR (additions IS NOT NULL AND deletions IS NOT NULL)
        );
