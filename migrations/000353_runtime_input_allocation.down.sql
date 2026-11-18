SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers, public.thread_runtimes IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM thread_runtimes WHERE job_id IS NOT NULL) THEN
        RAISE EXCEPTION 'retained lifecycle allocation prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
ALTER TABLE thread_runtimes
    DROP CONSTRAINT thread_runtimes_input_receipt_shapes_check,
    DROP CONSTRAINT thread_runtimes_own_provider_result_check,
    DROP CONSTRAINT thread_runtimes_input_allocation_check,
    DROP COLUMN provider_invocation_purpose,
    DROP COLUMN provider_invocation_nonce,
    DROP COLUMN provider_invocation_ordinal,
    DROP COLUMN first_provider_launch_marker,
    DROP COLUMN aborted_allocation_receipt,
    DROP COLUMN lease_loss_receipt,
    DROP COLUMN provider_terminal_settled_at,
    DROP COLUMN provider_terminal_result_digest,
    DROP COLUMN provider_terminal_result,
    DROP COLUMN input_dispatch_ids,
    DROP COLUMN allocation_expected_turn,
    DROP COLUMN allocation_digest,
    DROP COLUMN job_attempt_token,
    DROP COLUMN job_id;
DROP FUNCTION lifecycle143_valid_input_dispatch_set(uuid[]);
