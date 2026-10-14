-- Retention and the single-column FK both look up the referenced parent ID
-- without org_id. Keep existing tenant indexes and add this exact lookup.
-- Standalone CONCURRENTLY avoids blocking writes during the build. An invalid
-- interrupted index must be removed before repairing/retrying this migration.
CREATE INDEX CONCURRENTLY idx_slack_inbound_events_retention_delivery
    ON slack_inbound_events (webhook_delivery_id)
    WHERE webhook_delivery_id IS NOT NULL;
