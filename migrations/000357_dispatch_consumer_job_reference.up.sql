SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD CONSTRAINT session_input_dispatches_consumer_job_fk
    FOREIGN KEY (org_id, job_id) REFERENCES jobs (org_id, id) NOT VALID;
