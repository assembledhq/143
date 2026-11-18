-- Exact request membership includes legacy threadless requests. The immutable
-- request anchor and future canonical policy decide lane/context eligibility.
-- One existing hot parent per migration transaction; no producer is installed.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_workspace_compatibility
    ADD CONSTRAINT session_input_workspace_compatibility_request_scope_fk
    FOREIGN KEY (org_id, session_id, request_id)
    REFERENCES session_human_input_requests (org_id, session_id, id);
