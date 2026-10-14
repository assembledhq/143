-- Restore migration 300's temporary pause before indexes 301-307 are dropped.
-- If rollback is interrupted, cleanup remains paused until up309 resumes it
-- or down300 restores the original functions. No unindexed catch-up runs.
CREATE OR REPLACE FUNCTION delete_expired_webhook_deliveries(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    RETURN 0;
END;
$$;

CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
BEGIN
    RETURN 0;
END;
$$;
