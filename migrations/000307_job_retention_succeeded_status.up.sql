-- Successful jobs use succeeded; cancelled and dead-letter history is retained.
-- Limit each call to one batch so restoring a large historical succeeded
-- backlog cannot turn one cleanup invocation into an unbounded transaction.
-- A bounded worker sweep invokes this independently committed batch repeatedly.
-- Concurrent sweeps skip claimed rows instead of blocking on the same batch.
-- A recheck receipt retains its exact job identity beyond normal job TTL.
-- Filter protected rows inside the batch candidate subquery so a batch of
-- old retained jobs cannot starve deletion of unrelated expired jobs.
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
DECLARE batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN RETURN 0; END IF;
    WITH candidates AS MATERIALIZED (
        SELECT j.id FROM jobs j
        WHERE j.status IN ('succeeded','failed')
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
        ORDER BY j.updated_at,j.id
        LIMIT 10000
        FOR UPDATE OF j SKIP LOCKED
    )
    DELETE FROM jobs j USING candidates c WHERE j.id=c.id;
    GET DIAGNOSTICS batch_deleted = ROW_COUNT;
    RETURN batch_deleted;
END;
$$;
