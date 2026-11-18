-- One existing hot parent per migration transaction; dispatch has no producer.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_dispatches
    ADD CONSTRAINT session_input_dispatches_session_scope_fk
    FOREIGN KEY (org_id, session_id) REFERENCES sessions (org_id, id);
