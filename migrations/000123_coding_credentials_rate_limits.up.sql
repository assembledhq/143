ALTER TABLE coding_credentials
    ADD COLUMN rate_limited_until timestamptz,
    ADD COLUMN rate_limited_observed_at timestamptz,
    ADD COLUMN rate_limit_message text;

CREATE INDEX idx_coding_credentials_rate_limited_until
    ON coding_credentials (rate_limited_until)
    WHERE rate_limited_until IS NOT NULL;
