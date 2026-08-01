-- Removed readiness data cannot be reconstructed. This down migration restores
-- only the shared lease contract; restoring application readiness behavior also
-- requires restoring the six readiness tables and their data from backup.
ALTER TABLE session_changeset_leases
    DROP CONSTRAINT session_changeset_leases_holder_type_check,
    ADD CONSTRAINT session_changeset_leases_holder_type_check
        CHECK (holder_type IN ('agent_turn', 'materialize', 'publish', 'restack', 'readiness', 'preview'));
