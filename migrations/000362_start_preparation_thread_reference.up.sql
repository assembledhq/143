SET LOCAL lock_timeout = '5s';
ALTER TABLE session_runtime_start_preparations
    ADD CONSTRAINT start_preparations_thread_fk
    FOREIGN KEY (org_id,session_id,thread_id) REFERENCES session_threads(org_id,session_id,id) NOT VALID;
