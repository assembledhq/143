-- Standalone concurrent build: do not hold the column installer's session lock
-- across the heap scan. Invalid leftovers must fail, not be silently accepted.
CREATE UNIQUE INDEX CONCURRENTLY idx_sessions_workspace_recovery_id
    ON sessions(org_id,workspace_recovery_id)
    WHERE workspace_recovery_id IS NOT NULL;
