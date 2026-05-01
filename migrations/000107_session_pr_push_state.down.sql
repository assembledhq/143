ALTER TABLE pull_requests DROP COLUMN IF EXISTS head_ref;
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_pr_push_state;
ALTER TABLE sessions DROP COLUMN IF EXISTS pr_push_error;
ALTER TABLE sessions DROP COLUMN IF EXISTS pr_push_state;
