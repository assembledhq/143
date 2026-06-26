ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_pr_push_error_code;
ALTER TABLE sessions DROP COLUMN IF EXISTS pr_push_error_code;
