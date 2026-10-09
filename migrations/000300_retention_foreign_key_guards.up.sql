-- Preserve durable inbound and executor history while deleting unrelated expired parents.
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
            SELECT w.id FROM webhook_deliveries w
            WHERE w.created_at < now() - make_interval(days => p_retention_days)
              -- Match the actual single-column FKs, including any cross-org
              -- reference. Protected parents must not occupy the batch limit.
              AND NOT EXISTS (SELECT 1 FROM slack_inbound_events s WHERE s.webhook_delivery_id=w.id)
              AND NOT EXISTS (SELECT 1 FROM pagerduty_inbound_events p WHERE p.webhook_delivery_id=w.id)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;


-- A recheck receipt retains its exact job identity beyond normal job TTL.
-- Filter protected rows inside the batch candidate subquery so a batch of
-- old retained jobs cannot starve deletion of unrelated expired jobs.
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
DECLARE total_deleted bigint := 0; batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN RETURN 0; END IF;
    LOOP
        DELETE FROM jobs WHERE id IN (
            SELECT j.id FROM jobs j
            WHERE j.status IN ('completed','failed')
              AND j.updated_at < now() - make_interval(days => p_retention_days)
              -- Executor history retains its exact job beyond normal TTL,
              -- including terminal executors and single-column cross-org FKs.
              AND NOT EXISTS (SELECT 1 FROM session_executors e WHERE e.job_id=j.id)
              AND NOT EXISTS (SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=j.org_id AND d.job_id=j.id)
              AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments a
                  WHERE a.org_id=j.org_id AND a.review_scope='full' AND a.status IN ('running','publishing')
                  AND a.result_origin IS NOT NULL AND j.job_type='run_code_review'
                  AND j.payload->>'session_id'=a.session_id::text
                  AND j.payload->>'review_output_key'=a.publication_key)
            LIMIT 10000
        );
        GET DIAGNOSTICS batch_deleted = ROW_COUNT;
        total_deleted := total_deleted + batch_deleted;
        EXIT WHEN batch_deleted < 10000;
    END LOOP;
    RETURN total_deleted;
END;
$$;
