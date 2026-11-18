SET LOCAL lock_timeout = '5s';

-- Validation uses a separate transaction from the short catalog installation.
ALTER TABLE session_threads VALIDATE CONSTRAINT session_threads_admission_claim_pair_check;
ALTER TABLE session_threads VALIDATE CONSTRAINT session_threads_admission_claim_scope_fk;
