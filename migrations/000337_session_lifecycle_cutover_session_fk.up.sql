-- One existing hot-parent FK per transaction; both child keys are nonnull.
SET LOCAL lock_timeout = '5s';
ALTER TABLE session_lifecycle_cutovers
    ADD CONSTRAINT session_lifecycle_cutovers_session_scope_fk
    FOREIGN KEY (org_id, session_id) REFERENCES sessions (org_id, id);
