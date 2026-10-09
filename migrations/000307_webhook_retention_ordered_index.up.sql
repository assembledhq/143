-- Support deterministic oldest-first batches without sorting the webhook backlog.
-- Cleanup is still paused by migration 300. Build outside a transaction, and
-- require invalid interrupted builds to be removed before retrying migration.
CREATE INDEX CONCURRENTLY idx_webhook_deliveries_retention_ordered
    ON webhook_deliveries (created_at, id);
