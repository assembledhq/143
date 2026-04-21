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

-- Supports the startup reconciler's search for orphaned containers: rows with
-- container_id set but no active turn hold, paired with a no-preview-hold
-- check on preview_instances. Partial index keeps the index small since the
-- common case is container_id IS NULL.
CREATE INDEX IF NOT EXISTS idx_sessions_orphaned_containers
    ON sessions (id)
    WHERE container_id IS NOT NULL AND turn_holding_container = FALSE;
