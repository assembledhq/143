-- Recheck at the allowance-removal boundary even after a successful highest down.
-- A retained new-code row makes rollback a one-way door: use a forward fix;
-- never erase history, rewrite the classification or force the migration ledger.
SET LOCAL lock_timeout = '5s';
LOCK TABLE code_review_session_metadata, caller_send_intent_control,
    code_review_agent_results, session_review_loops, session_review_loop_passes,
    review_loop_start_intents, caller_send_command_stamps,
    session_publications, automation_runs IN ACCESS EXCLUSIVE MODE;
SELECT caller143_assert_send_rollback_safe();

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT code_review_session_metadata_status_code_v2_check;
ALTER TABLE code_review_session_metadata
    ADD CONSTRAINT code_review_session_metadata_status_code_check
    CHECK (status_code IS NULL OR status_code IN ('github_rate_limited','github_unavailable','reviewer_failed','worker_failed','review_loop_detected'));

CREATE OR REPLACE FUNCTION caller143_require_send_intents_enabled() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM caller_send_intent_control
    WHERE singleton AND schema_ready AND intents_enabled FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'caller_send_intents_disabled' USING ERRCODE = '55000';
    END IF;
END;
$$;
CREATE OR REPLACE FUNCTION caller143_assert_send_rollback_safe() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM code_review_agent_results WHERE dispatch_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM session_review_loops WHERE startup_send_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE caller143_start_unresolved(s.org_id,s.id)) THEN
        RAISE EXCEPTION 'unresolved_caller_send_prevents_rollback' USING ERRCODE = '55000';
    END IF;
END;
$$;
ALTER TABLE caller_send_intent_control
    DROP CONSTRAINT caller_send_dispatch_status_enablement_check,
    DROP COLUMN dispatch_status_ready;
