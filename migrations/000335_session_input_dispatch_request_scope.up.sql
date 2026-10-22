-- Exact request membership also represents legacy threadless requests. Future
-- canonical writers must independently verify null-safe request/lane equality.
-- One existing hot parent per migration transaction; dispatch has no producer.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD CONSTRAINT session_input_dispatches_request_scope_fk
    FOREIGN KEY (org_id, session_id, request_id)
    REFERENCES session_human_input_requests (org_id, session_id, id);
