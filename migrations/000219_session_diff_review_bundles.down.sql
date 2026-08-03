-- Drops whichever naming this installation carries: a fresh database has the
-- review_bundle_* columns this migration created, while a database that was
-- expanded and then contracted by migration 275 carries the legacy names.
DROP INDEX IF EXISTS idx_session_diff_snapshots_review_bundle_key;
DROP INDEX IF EXISTS idx_session_diff_snapshots_review_artifact_key;

ALTER TABLE session_diff_snapshots
    DROP COLUMN IF EXISTS review_bundle_truncated,
    DROP COLUMN IF EXISTS review_bundle_skipped_count,
    DROP COLUMN IF EXISTS review_bundle_file_count,
    DROP COLUMN IF EXISTS review_bundle_uncompressed_bytes,
    DROP COLUMN IF EXISTS review_bundle_compressed_bytes,
    DROP COLUMN IF EXISTS review_bundle_version,
    DROP COLUMN IF EXISTS review_bundle_key,
    DROP COLUMN IF EXISTS review_artifact_truncated,
    DROP COLUMN IF EXISTS review_artifact_skipped_count,
    DROP COLUMN IF EXISTS review_artifact_file_count,
    DROP COLUMN IF EXISTS review_artifact_uncompressed_bytes,
    DROP COLUMN IF EXISTS review_artifact_compressed_bytes,
    DROP COLUMN IF EXISTS review_artifact_version,
    DROP COLUMN IF EXISTS review_artifact_key;
