-- Revert to the pre-CircleCI provider set. Any existing 'circleci' rows
-- must be removed before the down migration will succeed, mirroring the
-- standard "if you've already used the new value, you opted in" rule.

ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion'
    ));
