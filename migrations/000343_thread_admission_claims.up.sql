-- Keep errors in the driver's implicit transaction so diagnostics remain usable.
SET LOCAL lock_timeout = '5s';

-- No backfill or writer. Install metadata without scanning the hot lane table.
ALTER TABLE session_threads
    ADD COLUMN admission_claim_dispatch_id uuid,
    ADD COLUMN admission_claim_token uuid,
    ADD CONSTRAINT session_threads_admission_claim_pair_check
        CHECK ((admission_claim_dispatch_id IS NULL) = (admission_claim_token IS NULL)) NOT VALID,
    ADD CONSTRAINT session_threads_admission_claim_scope_fk
        FOREIGN KEY (org_id, session_id, id, admission_claim_dispatch_id, admission_claim_token)
        REFERENCES session_input_dispatches (org_id, session_id, thread_id, id, admission_claim_token)
        NOT VALID;
