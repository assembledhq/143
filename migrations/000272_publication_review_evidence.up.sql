-- Add revision-bound evidence for publication review loops.
--
-- The CHECK and FOREIGN KEY constraints are installed NOT VALID here so their
-- validation scans do not run while the ACCESS EXCLUSIVE catalog locks are
-- held; migration 000273 validates them under a SHARE UPDATE EXCLUSIVE lock
-- that concurrent writers can tolerate.
--
-- Note on locking: the UNIQUE constraint and the index at the bottom of this
-- file cannot be deferred that way — a UNIQUE constraint has no NOT VALID form,
-- and our migrator wraps each file in a single transaction, which rules out
-- CREATE INDEX CONCURRENTLY. Both therefore build under ACCESS EXCLUSIVE and
-- block writes to their table for the duration.
--
-- Deploy check: `session_review_loops` holds one row per review loop and
-- `session_publications` one row per changeset publication — both narrow tables
-- with no per-pass, per-message, or per-turn fan-out. Confirm their row counts
-- before rolling this out; at the low-hundreds-of-thousands scale these builds
-- are sub-second, and if either has grown beyond that, split the index build
-- into its own release with the transaction disabled so it can run
-- CONCURRENTLY.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_id_org_id_key UNIQUE (id, org_id);

ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS chk_session_review_loops_source;
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
