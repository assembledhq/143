DROP INDEX IF EXISTS idx_session_diff_snapshots_review_artifact_key;

ALTER TABLE session_diff_snapshots
    DROP COLUMN IF EXISTS review_artifact_truncated,
    DROP COLUMN IF EXISTS review_artifact_skipped_count,
    DROP COLUMN IF EXISTS review_artifact_file_count,
    DROP COLUMN IF EXISTS review_artifact_uncompressed_bytes,
    DROP COLUMN IF EXISTS review_artifact_compressed_bytes,
    DROP COLUMN IF EXISTS review_artifact_version,
    DROP COLUMN IF EXISTS review_artifact_key;
