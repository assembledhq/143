SET LOCAL lock_timeout = '5s';

-- Roll back readers and every claim writer before removing receipt metadata.
ALTER TABLE session_input_dispatches
    DROP COLUMN admission_claim_turn,
    DROP COLUMN admission_claim_token;
