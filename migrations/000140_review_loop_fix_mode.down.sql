ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS chk_session_review_loops_fix_mode;

ALTER TABLE session_review_loops
    DROP COLUMN IF EXISTS fix_mode;
