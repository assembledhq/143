ALTER TABLE session_threads
    DROP CONSTRAINT IF EXISTS chk_session_threads_filesystem_mode,
    DROP CONSTRAINT IF EXISTS chk_session_threads_execution_mode,
    DROP COLUMN IF EXISTS filesystem_mode,
    DROP COLUMN IF EXISTS execution_mode;

ALTER TABLE code_review_policies
    DROP COLUMN IF EXISTS inheritance;
