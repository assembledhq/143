ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT IF EXISTS code_review_session_metadata_phase_check,
    DROP CONSTRAINT IF EXISTS code_review_session_metadata_status_code_check,
    DROP COLUMN IF EXISTS retryable_failure,
    DROP COLUMN IF EXISTS last_error_at,
    DROP COLUMN IF EXISTS retry_at,
    DROP COLUMN IF EXISTS status_message,
    DROP COLUMN IF EXISTS status_code,
    DROP COLUMN IF EXISTS phase;
