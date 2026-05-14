DROP INDEX IF EXISTS idx_coding_credentials_rate_limited_until;

ALTER TABLE coding_credentials
    DROP COLUMN IF EXISTS rate_limit_message,
    DROP COLUMN IF EXISTS rate_limited_observed_at,
    DROP COLUMN IF EXISTS rate_limited_until;
