ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_pr_creation_state;
ALTER TABLE sessions DROP COLUMN IF EXISTS pr_creation_error;
ALTER TABLE sessions DROP COLUMN IF EXISTS pr_creation_state;
