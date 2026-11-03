-- MATCH SIMPLE is intentional for a job-only binding. The consumer-shape CHECK
-- requires a nonnull exact session/lane whenever runtime_id is present.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD CONSTRAINT session_input_dispatches_consumer_runtime_fk
    FOREIGN KEY (org_id, session_id, thread_id, runtime_id)
    REFERENCES thread_runtimes (org_id, session_id, thread_id, id) NOT VALID;
