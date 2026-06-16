ALTER TABLE automations
    ADD COLUMN github_event_filters jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE automation_trigger_dedupes (
    org_id uuid NOT NULL REFERENCES organizations(id),
    automation_id uuid NOT NULL REFERENCES automations(id),
    dedupe_key text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, automation_id, dedupe_key)
);

CREATE INDEX idx_automation_trigger_dedupes_expires_at
    ON automation_trigger_dedupes (expires_at);
