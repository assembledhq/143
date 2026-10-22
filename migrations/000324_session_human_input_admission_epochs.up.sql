-- Passive provenance only. Legacy requests, including threadless requests, keep
-- unknown epochs. Never infer admission identity from current parent state.
-- Canonical admission, readiness and activation guards are separate work.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_human_input_requests
    ADD COLUMN admission_session_epoch bigint,
    ADD COLUMN admission_thread_epoch bigint;
