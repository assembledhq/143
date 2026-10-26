SET LOCAL lock_timeout = '5s';
ALTER TABLE session_runtime_start_preparations
    ADD CONSTRAINT start_preparations_request_fk
    FOREIGN KEY (org_id,session_id,request_id) REFERENCES session_human_input_requests(org_id,session_id,id) NOT VALID;
