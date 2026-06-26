ALTER TABLE session_diff_snapshots
    ADD COLUMN review_artifact_key text,
    ADD COLUMN review_artifact_version integer,
    ADD COLUMN review_artifact_compressed_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN review_artifact_uncompressed_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN review_artifact_file_count integer NOT NULL DEFAULT 0,
    ADD COLUMN review_artifact_skipped_count integer NOT NULL DEFAULT 0,
    ADD COLUMN review_artifact_truncated boolean NOT NULL DEFAULT false;

CREATE INDEX idx_session_diff_snapshots_review_artifact_key
    ON session_diff_snapshots (org_id, session_id, review_artifact_key)
    WHERE review_artifact_key IS NOT NULL;
