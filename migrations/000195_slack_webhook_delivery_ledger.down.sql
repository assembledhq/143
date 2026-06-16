ALTER TABLE slack_inbound_events
    DROP CONSTRAINT IF EXISTS chk_slack_inbound_events_status;

ALTER TABLE slack_inbound_events
    ADD CONSTRAINT chk_slack_inbound_events_status
    CHECK (status IN ('received', 'enqueued', 'processed', 'failed'));

DROP INDEX IF EXISTS idx_slack_inbound_events_webhook_delivery;

ALTER TABLE slack_inbound_events
    DROP COLUMN IF EXISTS webhook_delivery_id;
