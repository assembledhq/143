-- The migration driver sends this whole file as one implicit transaction.
SET LOCAL lock_timeout = '5s';

-- Passive receipt slots: NULL remains unknown, not a current-turn inference.
ALTER TABLE session_input_dispatches
    ADD COLUMN admission_claim_token uuid,
    ADD COLUMN admission_claim_turn integer;
