ALTER TABLE automations
    ADD COLUMN publish_policy TEXT NOT NULL DEFAULT 'pull_request';

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_publish_policy
    CHECK (publish_policy IN ('pull_request', 'none'));
