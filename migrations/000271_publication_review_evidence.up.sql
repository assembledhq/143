-- Add revision-bound evidence for publication review loops. Constraints are
-- installed NOT VALID here so existing rows are not scanned while the
-- ACCESS EXCLUSIVE catalog locks are held; migration 000272 validates them.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_id_org_id_key UNIQUE (id, org_id);

ALTER TABLE session_review_loops
    DROP CONSTRAINT chk_session_review_loops_source;
ALTER TABLE session_review_loops
    ADD CONSTRAINT chk_session_review_loops_source
        CHECK (source IN ('manual', 'automation', 'publication')) NOT VALID;

ALTER TABLE session_review_loops
    ADD COLUMN changeset_id uuid,
    ADD COLUMN workspace_revision bigint,
    ADD COLUMN desired_head_sha text;

ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id)
        ON DELETE CASCADE NOT VALID,
    ADD CONSTRAINT session_review_loops_publication_evidence_check
        CHECK (
            source <> 'publication'
            OR (
                changeset_id IS NOT NULL
                AND workspace_revision IS NOT NULL
                AND desired_head_sha IS NOT NULL
            )
        ) NOT VALID;

ALTER TABLE session_publications
    ADD COLUMN review_max_passes integer,
    ADD COLUMN review_loop_id uuid,
    ADD COLUMN review_workspace_revision bigint,
    ADD COLUMN review_desired_head_sha text;

ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_review_loop_scope_fkey
        FOREIGN KEY (review_loop_id, org_id)
        REFERENCES session_review_loops(id, org_id) NOT VALID,
    ADD CONSTRAINT session_publications_review_passes_check
        CHECK (review_max_passes IS NULL OR review_max_passes BETWEEN 1 AND 5) NOT VALID,
    ADD CONSTRAINT session_publications_review_evidence_check
        CHECK (
            review_loop_id IS NULL
            OR (
                review_max_passes IS NOT NULL
                AND review_workspace_revision IS NOT NULL
                AND review_desired_head_sha IS NOT NULL
            )
        ) NOT VALID;

CREATE INDEX idx_session_publications_review_loop
    ON session_publications (org_id, review_loop_id)
    WHERE review_loop_id IS NOT NULL;
