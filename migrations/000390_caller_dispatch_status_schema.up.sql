-- Passive prerequisite: require explicitly disabled participation before changing
-- its interlock. No migration opts a database into caller-send participation.
SET LOCAL lock_timeout = '5s';
LOCK TABLE code_review_session_metadata IN ACCESS EXCLUSIVE MODE;
LOCK TABLE caller_send_intent_control IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM caller_send_intent_control WHERE intents_enabled) THEN
        RAISE EXCEPTION 'caller_send_intents_must_be_disabled_for_status_schema' USING ERRCODE = '55000';
    END IF;
END;
$$;

ALTER TABLE caller_send_intent_control
    ADD COLUMN dispatch_status_ready boolean NOT NULL DEFAULT false;
ALTER TABLE caller_send_intent_control
    ADD CONSTRAINT caller_send_dispatch_status_enablement_check
    CHECK (NOT intents_enabled OR (schema_ready AND dispatch_status_ready));

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT code_review_session_metadata_status_code_check;
ALTER TABLE code_review_session_metadata
    ADD CONSTRAINT code_review_session_metadata_status_code_v2_check
    CHECK (status_code IS NULL OR status_code IN (
        'github_rate_limited','github_unavailable','reviewer_failed','worker_failed',
        'review_loop_detected','code_reviewer_dispatch_unresolved')) NOT VALID;

CREATE OR REPLACE FUNCTION caller143_require_send_intents_enabled() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM caller_send_intent_control
    WHERE singleton AND schema_ready AND dispatch_status_ready AND intents_enabled FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'caller_send_intents_disabled' USING ERRCODE = '55000';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION caller143_assert_send_rollback_safe() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    -- This is deliberately independent of current due tuples. Retained business
    -- history is never erased or mapped to an older code to make rollback pass.
    IF EXISTS (SELECT 1 FROM code_review_session_metadata
        WHERE status_code = 'code_reviewer_dispatch_unresolved') THEN
        RAISE EXCEPTION 'retained_dispatch_unresolved_status_prevents_rollback' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM code_review_agent_results WHERE dispatch_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM session_review_loops WHERE startup_send_due_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM review_loop_start_intents s WHERE caller143_start_unresolved(s.org_id,s.id)) THEN
        RAISE EXCEPTION 'unresolved_caller_send_prevents_rollback' USING ERRCODE = '55000';
    END IF;
END;
$$;
