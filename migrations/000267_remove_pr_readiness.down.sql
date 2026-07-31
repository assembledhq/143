-- Removed readiness data cannot be reconstructed. This down migration restores
-- only the shared job/lease contracts; rolling back application code also
-- requires restoring the readiness tables from backup.
DROP TRIGGER IF EXISTS trg_reject_removed_pr_readiness_jobs ON jobs;
DROP FUNCTION IF EXISTS reject_removed_pr_readiness_jobs();

ALTER TABLE session_changeset_leases
    DROP CONSTRAINT session_changeset_leases_holder_type_check,
    ADD CONSTRAINT session_changeset_leases_holder_type_check
        CHECK (holder_type IN ('agent_turn', 'materialize', 'publish', 'restack', 'readiness', 'preview'));
