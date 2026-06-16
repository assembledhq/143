ALTER TABLE slack_inbound_events
    ADD COLUMN IF NOT EXISTS webhook_delivery_id uuid REFERENCES webhook_deliveries(id);

CREATE INDEX IF NOT EXISTS idx_slack_inbound_events_webhook_delivery
    ON slack_inbound_events (org_id, webhook_delivery_id)
    WHERE webhook_delivery_id IS NOT NULL;

ALTER TABLE slack_inbound_events
    DROP CONSTRAINT IF EXISTS chk_slack_inbound_events_status;

ALTER TABLE slack_inbound_events
    ADD CONSTRAINT chk_slack_inbound_events_status
    CHECK (status IN ('received', 'enqueued', 'processed', 'failed', 'ignored'));
