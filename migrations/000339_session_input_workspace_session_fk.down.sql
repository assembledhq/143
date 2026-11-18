-- FK removal can require stronger parent locks than installation.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_input_workspace_compatibility
    DROP CONSTRAINT session_input_workspace_compatibility_session_scope_fk;
