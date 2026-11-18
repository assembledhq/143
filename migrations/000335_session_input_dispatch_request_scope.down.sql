SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    DROP CONSTRAINT session_input_dispatches_request_scope_fk;
