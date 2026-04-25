ALTER TABLE sessions
    DROP COLUMN IF EXISTS git_identity_user_id,
    DROP COLUMN IF EXISTS git_identity_source;
