-- FK removal can require stronger parent locks than installation.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_lifecycle_cutovers
    DROP CONSTRAINT session_lifecycle_cutovers_session_scope_fk;
