ALTER TABLE session_diff_snapshots
    ADD COLUMN review_bundle_key text,
    ADD COLUMN review_bundle_version integer,
    ADD COLUMN review_bundle_compressed_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN review_bundle_uncompressed_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN review_bundle_file_count integer NOT NULL DEFAULT 0,
    ADD COLUMN review_bundle_skipped_count integer NOT NULL DEFAULT 0,
    ADD COLUMN review_bundle_truncated boolean NOT NULL DEFAULT false;

CREATE INDEX idx_session_diff_snapshots_review_bundle_key
    ON session_diff_snapshots (org_id, session_id, review_bundle_key)
    WHERE review_bundle_key IS NOT NULL;
