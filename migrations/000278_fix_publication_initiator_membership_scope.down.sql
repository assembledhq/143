SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    DROP CONSTRAINT IF EXISTS session_publications_initiator_scope_fkey;

ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_initiator_scope_fkey
        FOREIGN KEY (initiated_by_user_id, org_id)
        REFERENCES users(id, org_id)
        NOT VALID;
