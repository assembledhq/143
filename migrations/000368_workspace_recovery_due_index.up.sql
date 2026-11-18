-- Standalone concurrent build; F must check validity before installing a writer.
CREATE INDEX CONCURRENTLY idx_sessions_workspace_recovery_due
    ON sessions(workspace_recovery_next_attempt_at,id)
    WHERE workspace_recovery_id IS NOT NULL;
