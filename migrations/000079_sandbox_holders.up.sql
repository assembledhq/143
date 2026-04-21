-- Holder columns for the shared sandbox container lifecycle.
--
-- The agent's sandbox container used to be owned entirely by the active turn:
-- created at turn start, destroyed at turn end. Previews could not run between
-- turns because the container was gone.
--
-- The new model lets the turn and the preview act as independent "holders" of
-- the same container. The container is destroyed only when both holders are
-- false. These two booleans are the durable refcount; together with
-- sessions.container_id they tell us whether a live container exists and who
-- is keeping it alive.
--
-- Defaults are FALSE because every existing session is between turns (no turn
-- hold) and preview_instances rows that predate this change were never
-- hydrating the container themselves (no preview hold).
ALTER TABLE sessions
    ADD COLUMN turn_holding_container BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE preview_instances
    ADD COLUMN preview_holding_container BOOLEAN NOT NULL DEFAULT FALSE;

-- Supports the startup reconciler's search for orphaned containers: rows
-- with container_id set, paired with a no-preview-hold check on
-- preview_instances. turn_holding_container is deliberately NOT part of the
-- predicate so crashed-turn rows (stuck with turn_holding_container=TRUE)
-- are still reachable — the reconciler uses IsAlive as the ground-truth
-- liveness gate and ClearContainerID resets the stale flag atomically.
-- Partial index keeps the index small since the common case is
-- container_id IS NULL (a session between turns).
CREATE INDEX IF NOT EXISTS idx_sessions_orphaned_containers
    ON sessions (id)
    WHERE container_id IS NOT NULL;

-- Supports the "does any preview still hold this session's sandbox?" probe
-- issued by ReleaseTurnHold, FinalizeContainerDestroy, ListOrphanedContainers,
-- and the release-preview-hold CAS in preview_store. Without this partial
-- index those queries fall back to a full scan of preview_instances, which
-- grows unbounded as terminal previews accumulate. Only TRUE rows are
-- indexed (the common case is FALSE once a preview stops), so the index
-- stays small and lookups are O(live-holders-per-session).
CREATE INDEX IF NOT EXISTS idx_preview_instances_active_hold
    ON preview_instances (session_id, org_id)
    WHERE preview_holding_container = TRUE;
