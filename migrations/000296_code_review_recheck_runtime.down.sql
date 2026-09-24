DROP TABLE IF EXISTS code_review_recheck_dispatches;
CREATE OR REPLACE FUNCTION delete_expired_completed_jobs(p_retention_days int)
RETURNS bigint LANGUAGE plpgsql SET search_path = public AS $$
DECLARE total_deleted bigint := 0; batch_deleted bigint;
BEGIN
    IF p_retention_days <= 0 THEN RETURN 0; END IF;
    LOOP
        DELETE FROM jobs WHERE id IN (
            SELECT id FROM jobs
            WHERE status IN ('completed','failed')
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
DROP TRIGGER IF EXISTS trg_code_review_owned_thread_structure ON session_threads;
DROP FUNCTION IF EXISTS guard_code_review_owned_thread_structure();
DROP TRIGGER IF EXISTS trg_code_review_owned_preview ON preview_instances;
DROP FUNCTION IF EXISTS guard_code_review_owned_preview();
DROP TRIGGER IF EXISTS trg_code_review_owned_message ON session_messages;
DROP FUNCTION IF EXISTS guard_code_review_owned_message();
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_code_review_owner_pr_fk;
ALTER TABLE sessions DROP COLUMN IF EXISTS code_review_owner_pr_id;
DROP INDEX IF EXISTS code_review_recheck_jobs_org_id;
DROP INDEX IF EXISTS code_review_recheck_threads_org_id;
