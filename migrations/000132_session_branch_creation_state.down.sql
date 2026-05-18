ALTER TABLE sessions DROP CONSTRAINT IF EXISTS chk_sessions_branch_creation_state;

ALTER TABLE sessions DROP COLUMN IF EXISTS branch_url;
ALTER TABLE sessions DROP COLUMN IF EXISTS branch_creation_error;
ALTER TABLE sessions DROP COLUMN IF EXISTS branch_creation_state;
