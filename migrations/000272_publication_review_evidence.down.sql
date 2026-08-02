SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_session_publications_review_loop;

ALTER TABLE session_publications
    DROP CONSTRAINT IF EXISTS session_publications_review_evidence_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_passes_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_loop_scope_fkey,
    DROP COLUMN IF EXISTS review_desired_head_sha,
    DROP COLUMN IF EXISTS review_workspace_revision,
    DROP COLUMN IF EXISTS review_loop_id,
    DROP COLUMN IF EXISTS review_max_passes;

-- Preserve review history and pass children when narrowing the source enum.
UPDATE session_review_loops SET source = 'manual' WHERE source = 'publication';

ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS session_review_loops_publication_evidence_check,
    DROP CONSTRAINT IF EXISTS session_review_loops_changeset_scope_fkey,
    DROP COLUMN IF EXISTS desired_head_sha,
    DROP COLUMN IF EXISTS workspace_revision,
    DROP COLUMN IF EXISTS changeset_id,
    DROP CONSTRAINT IF EXISTS session_review_loops_id_org_id_key;

ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS chk_session_review_loops_source;
ALTER TABLE session_review_loops
    ADD CONSTRAINT chk_session_review_loops_source
        CHECK (source IN ('manual', 'automation'));
