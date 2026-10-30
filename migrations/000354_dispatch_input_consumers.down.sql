SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers, public.session_input_dispatches IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM session_input_dispatches WHERE admission_state IS NOT NULL) THEN
        RAISE EXCEPTION 'retained lifecycle input prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
DROP INDEX idx_session_input_dispatches_control_due;
DROP INDEX idx_session_input_dispatches_bound_job;
DROP INDEX idx_session_input_dispatches_consumer_runtime;
DROP INDEX idx_session_input_dispatches_consumer_job;
DROP INDEX idx_session_input_dispatches_consumer_lane;
DROP INDEX idx_session_input_dispatches_waiting_due;
DROP INDEX idx_session_input_dispatches_bound_request;
ALTER TABLE session_input_dispatches
    DROP CONSTRAINT session_input_dispatches_control_shape_check,
    DROP CONSTRAINT session_input_dispatches_consumer_shape_check,
    DROP COLUMN control_refund_receipt_id,
    DROP COLUMN control_next_attempt_at,
    DROP COLUMN control_snapshot,
    DROP COLUMN control_state,
    DROP COLUMN control_version,
    DROP COLUMN control_receipt_id,
    DROP COLUMN reason,
    DROP COLUMN deadline_at,
    DROP COLUMN next_attempt_at,
    DROP COLUMN runtime_lease_token,
    DROP COLUMN job_lock_token,
    DROP COLUMN runtime_id,
    DROP COLUMN job_id,
    DROP COLUMN consumer_kind,
    DROP COLUMN admission_state;
