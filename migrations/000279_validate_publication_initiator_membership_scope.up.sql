SET LOCAL lock_timeout = '5s';

ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_initiator_scope_fkey;
