-- Add 'circleci' to the integrations.provider CHECK constraint.
-- The constraint was created by 000035 with the original five-provider set
-- (github, sentry, linear, slack, notion). CircleCI joins as the sixth
-- supported provider; without this constraint update, INSERTs from the new
-- ConnectCircleCI handler would fail validation.

ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion', 'circleci'
    ));
