-- Readiness is not enablement. The release procedure must also require a clean
-- migration ledger at this version or later before explicitly enabling intents.
SET LOCAL lock_timeout = '5s';
LOCK TABLE code_review_session_metadata IN SHARE UPDATE EXCLUSIVE MODE;
LOCK TABLE caller_send_intent_control IN SHARE ROW EXCLUSIVE MODE;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
        WHERE conrelid = 'code_review_session_metadata'::regclass
            AND conname = 'code_review_session_metadata_status_code_v2_check' AND convalidated)
        OR EXISTS (SELECT 1 FROM pg_constraint
            WHERE conrelid = 'code_review_session_metadata'::regclass
                AND conname = 'code_review_session_metadata_status_code_check')
        OR NOT EXISTS (SELECT 1 FROM pg_constraint
            WHERE conrelid = 'caller_send_intent_control'::regclass
                AND conname = 'caller_send_dispatch_status_enablement_check' AND convalidated)
        OR NOT EXISTS (SELECT 1 FROM caller_send_intent_control WHERE singleton AND schema_ready) THEN
        RAISE EXCEPTION 'caller_dispatch_status_schema_not_ready' USING ERRCODE = '55000';
    END IF;
END;
$$;
UPDATE caller_send_intent_control SET dispatch_status_ready = true, updated_at = now()
WHERE singleton;
