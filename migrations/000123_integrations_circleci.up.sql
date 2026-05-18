ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion', 'circleci'
    ));
