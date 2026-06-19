-- Revert to the pre-mezmo provider set. Any existing 'mezmo' integration rows
-- must be removed before this runs, or VALIDATE will fail.
ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion', 'circleci'
    )) NOT VALID;

ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_provider;
