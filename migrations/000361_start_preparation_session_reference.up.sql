SET LOCAL lock_timeout = '5s';
ALTER TABLE session_runtime_start_preparations
    ADD CONSTRAINT start_preparations_session_fk
    FOREIGN KEY (org_id,session_id) REFERENCES sessions(org_id,id) NOT VALID;
