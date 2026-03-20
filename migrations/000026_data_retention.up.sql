-- Indexes to support the batched retention deletes below.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_created_at
    ON webhook_deliveries (created_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_logs_timestamp
    ON session_logs (timestamp);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jobs_status_updated_at
    ON jobs (status, updated_at)
    WHERE status IN ('completed', 'failed');

-- Delete old webhook deliveries in batches to avoid long-running locks.
CREATE OR REPLACE FUNCTION delete_expired_webhook_deliveries(p_retention_days int)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public
AS $$
DECLARE
    total_deleted bigint := 0;
    batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN
        RETURN 0;
    END IF;
    LOOP
        DELETE FROM webhook_deliveries
        WHERE id IN (
            SELECT id FROM webhook_deliveries
            WHERE created_at < now() - make_interval(days => p_retention_days)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;

-- Delete old session logs in batches to avoid long-running locks.
CREATE OR REPLACE FUNCTION delete_expired_session_logs(p_retention_days int)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public
AS $$
DECLARE
    total_deleted bigint := 0;
    batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN
        RETURN 0;
    END IF;
    LOOP
        DELETE FROM session_logs
        WHERE id IN (
            SELECT id FROM session_logs
            WHERE timestamp < now() - make_interval(days => p_retention_days)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;

-- Delete completed/failed jobs older than retention period in batches.
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint
LANGUAGE plpgsql
SET search_path = public
AS $$
DECLARE
    total_deleted bigint := 0;
    batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN
        RETURN 0;
    END IF;
    LOOP
        DELETE FROM jobs
        WHERE id IN (
            SELECT id FROM jobs
            WHERE status IN ('completed', 'failed')
              AND updated_at < now() - make_interval(days => p_retention_days)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;
