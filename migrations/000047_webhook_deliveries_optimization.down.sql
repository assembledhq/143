DROP INDEX IF EXISTS idx_webhook_deliveries_status_received;
DROP INDEX IF EXISTS idx_webhook_deliveries_seq;
ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS seq;
DROP INDEX IF EXISTS idx_webhook_deliveries_received_brin;
