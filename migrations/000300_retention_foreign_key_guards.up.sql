-- Pause global job and webhook cleanup while migrations 301-307 build their
-- concurrent supporting indexes. Old workers also call these functions during
-- the schema-first rollout and must not begin catch-up against unindexed FKs.
-- If migration is interrupted here, cleanup stays paused until migration 309
-- completes. Ordinary job execution and webhook ingestion are unaffected.
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
