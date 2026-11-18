SET LOCAL lock_timeout = '5s';

-- Never cascade into retained dispatch identity or legacy lane state.
ALTER TABLE session_threads
    DROP CONSTRAINT session_threads_admission_claim_scope_fk,
    DROP CONSTRAINT session_threads_admission_claim_pair_check,
    DROP COLUMN admission_claim_token,
    DROP COLUMN admission_claim_dispatch_id;
