SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers,
    public.jobs, public.thread_runtimes, public.session_input_dispatches IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM thread_runtimes WHERE job_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM session_input_dispatches WHERE job_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM jobs WHERE job_type='snapshot_object_cleanup') THEN
        RAISE EXCEPTION 'retained lifecycle consumer or resource obligation prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
ALTER TABLE thread_runtimes DROP CONSTRAINT thread_runtimes_consumer_job_fk;

-- Restore migration309's policy only after all newer references are absent.
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
DECLARE batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN RETURN 0; END IF;
    WITH candidates AS MATERIALIZED (
        SELECT j.id FROM jobs j
        WHERE j.status IN ('succeeded','failed')
          AND j.updated_at < now() - make_interval(days => p_retention_days)
          AND NOT EXISTS (SELECT 1 FROM session_executors e WHERE e.job_id=j.id)
          AND NOT EXISTS (SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=j.org_id AND d.job_id=j.id)
          AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments a
              WHERE a.org_id=j.org_id AND a.review_scope='full' AND a.status IN ('reserved','running','publishing')
              AND j.job_type='run_code_review'
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
