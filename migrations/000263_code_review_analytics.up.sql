ALTER TABLE code_review_session_metadata
    ADD COLUMN files_changed integer,
    ADD COLUMN additions integer,
    ADD COLUMN deletions integer,
    ADD COLUMN lines_changed integer,
    ADD COLUMN risk_reason_details jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_code_review_session_metadata_files_changed
        CHECK (files_changed IS NULL OR files_changed >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_additions
        CHECK (additions IS NULL OR additions >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_deletions
        CHECK (deletions IS NULL OR deletions >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_lines_changed
        CHECK (lines_changed IS NULL OR lines_changed >= 0),
    ADD CONSTRAINT chk_code_review_session_metadata_change_breakdown
        CHECK (
            (additions IS NULL AND deletions IS NULL)
            OR (
                additions IS NOT NULL
                AND deletions IS NOT NULL
                AND lines_changed IS NOT NULL
                AND lines_changed = additions + deletions
            )
        );

-- Recent review bodies include a stable "<lines> changed lines across <files>
-- files" fact. Recover those values so the first analytics report has a useful
-- historical sample; reviews that predate the fact remain NULL and are reported
-- separately from the size sample. The body does not preserve additions and
-- deletions independently, so those breakdowns begin with newly completed
-- reviews rather than inventing a historical split.
WITH extracted AS (
    SELECT
        id,
        (regexp_match(
            final_review_body,
            '([0-9]+) changed lines? across ([0-9]+) files?'
        ))[1]::integer AS lines_changed,
        (regexp_match(
            final_review_body,
            '([0-9]+) changed lines? across ([0-9]+) files?'
        ))[2]::integer AS files_changed
    FROM code_review_session_metadata
    WHERE final_review_body ~ '[0-9]+ changed lines? across [0-9]+ files?'
)
UPDATE code_review_session_metadata AS metadata
SET
    lines_changed = extracted.lines_changed,
    files_changed = extracted.files_changed
FROM extracted
WHERE metadata.id = extracted.id;

CREATE INDEX idx_code_review_metadata_analytics
    ON code_review_session_metadata (org_id, created_at DESC)
    WHERE status = 'completed';
