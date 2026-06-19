-- Add 'mezmo' to the integrations.provider CHECK constraint.
-- Uses NOT VALID + VALIDATE CONSTRAINT so the re-add doesn't hold an ACCESS
-- EXCLUSIVE lock while scanning the table: NOT VALID applies to new writes
-- immediately, then VALIDATE rescans existing rows under a weaker lock.
-- Widening the allowed set, so existing rows already satisfy it.
ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion', 'circleci', 'mezmo'
    )) NOT VALID;

ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_provider;
