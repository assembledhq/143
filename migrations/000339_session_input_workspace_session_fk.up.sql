-- One existing hot parent per migration transaction; no producer is installed.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_workspace_compatibility
    ADD CONSTRAINT session_input_workspace_compatibility_session_scope_fk
    FOREIGN KEY (org_id, session_id) REFERENCES sessions (org_id, id);
