-- Refuse to destroy durable archive receipts or expired acquisition identities.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM code_review_exports WHERE released_at IS NOT NULL OR slot IS NULL) THEN
        RAISE EXCEPTION 'export release metadata is durable; rollback requires a separately reviewed preservation plan';
    END IF;
END $$;
ALTER TABLE code_review_exports
    DROP CONSTRAINT code_review_exports_release_consistent,
    DROP COLUMN release_receipt,
    DROP COLUMN released_at,
    ALTER COLUMN slot SET NOT NULL;
