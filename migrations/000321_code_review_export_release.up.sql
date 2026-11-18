-- Keep acquisition identity and archive attestations after disposable payload cleanup.
ALTER TABLE code_review_exports
    ALTER COLUMN slot DROP NOT NULL,
    ADD COLUMN released_at timestamptz,
    ADD COLUMN release_receipt jsonb,
    ADD CONSTRAINT code_review_exports_release_consistent CHECK (
        (released_at IS NULL AND release_receipt IS NULL)
        OR (released_at IS NOT NULL AND release_receipt IS NOT NULL AND slot IS NULL)
    );
