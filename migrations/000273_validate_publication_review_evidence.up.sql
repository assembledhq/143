SET LOCAL lock_timeout = '5s';

ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT chk_session_review_loops_source;
ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT session_review_loops_changeset_scope_fkey;
ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT session_review_loops_publication_evidence_check;

ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_loop_scope_fkey;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_passes_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_evidence_check;
