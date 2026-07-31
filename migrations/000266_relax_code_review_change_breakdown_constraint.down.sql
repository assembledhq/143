ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT IF EXISTS chk_code_review_session_metadata_change_breakdown;

-- This is also the constraint created by the current 000263 migration. Keeping
-- the rollback compatible with both fresh and previously migrated databases
-- avoids referring to legacy columns that no longer exist on fresh installs.
ALTER TABLE code_review_session_metadata
    ADD CONSTRAINT chk_code_review_session_metadata_change_breakdown
        CHECK (
            (additions IS NULL AND deletions IS NULL)
            OR (additions IS NOT NULL AND deletions IS NOT NULL)
        );
