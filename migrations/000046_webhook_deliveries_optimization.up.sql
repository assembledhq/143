-- webhook_deliveries uses random UUID v4 PKs which cause B-tree fragmentation
-- on high-volume append workloads. A full PK type change is too invasive for
-- a production migration, so we add optimizations that mitigate the impact:

-- 1. BRIN index on received_at for efficient time-range scans.
--    BRIN is ideal for append-only, time-ordered data — tiny index, fast scans.
CREATE INDEX idx_webhook_deliveries_received_brin
    ON webhook_deliveries USING BRIN (received_at);

-- 2. Add a bigserial column as a monotonic clustering key.
--    This gives an ordered insert path for future queries that need
--    sequential scan efficiency without changing the PK.
ALTER TABLE webhook_deliveries ADD COLUMN seq bigserial;
CREATE INDEX idx_webhook_deliveries_seq ON webhook_deliveries (seq);

-- 3. Add missing index for retry/replay workers (documented in schema but never created).
CREATE INDEX idx_webhook_deliveries_status_received
    ON webhook_deliveries (status, received_at) WHERE status IN ('received', 'failed');
