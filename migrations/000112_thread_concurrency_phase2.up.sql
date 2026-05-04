-- Phase 2-4 multi-tab thread concurrency.
--
-- Adds the durable state required to run multiple agent tabs concurrently in
-- one shared sandbox: a per-tab base snapshot for thread-start checkpoints, a
-- cost meter, a pending-message counter so the composer can show queued sends,
-- and an explicit cancel-requested timestamp so a per-thread cancel is
-- distinguishable from a session-wide cancel.
--
-- Adds session_thread_file_events for "which tab touched which file" — this is
-- operational evidence used to show overlap badges in the tab strip and to
-- power the Changes view's "Touched by tab" / "Overlap with another tab"
-- filters. It is not security attribution.

ALTER TABLE session_threads
    ADD COLUMN base_snapshot_key text,
    ADD COLUMN cost_cents numeric(12, 4) NOT NULL DEFAULT 0,
    ADD COLUMN pending_message_count integer NOT NULL DEFAULT 0,
    ADD COLUMN cancel_requested_at timestamptz;

-- Bound the new counters so a logic bug cannot push the row into a state the
-- UI cannot reason about (negative cost, negative queue depth). Mirrors the
-- "validate at the boundary" pattern used elsewhere in the schema.
ALTER TABLE session_threads
    ADD CONSTRAINT chk_session_threads_cost_cents_nonneg CHECK (cost_cents >= 0),
    ADD CONSTRAINT chk_session_threads_pending_messages_nonneg CHECK (pending_message_count >= 0);

CREATE INDEX idx_session_threads_running
    ON session_threads (org_id, session_id)
    WHERE status IN ('pending', 'running', 'awaiting_input');

-- File-touch events for tab attribution. event_type is one of
-- 'created', 'modified', 'deleted'. before_hash/after_hash are git blob OIDs
-- when available. Multiple events per (session, path) are expected — the
-- timeline is the audit log; the latest row wins for "current" UI state.
CREATE TABLE session_thread_file_events (
    id          bigserial   PRIMARY KEY,
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    session_id  uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id   uuid        REFERENCES session_threads(id) ON DELETE SET NULL,
    turn        integer     NOT NULL DEFAULT 0,
    path        text        NOT NULL,
    event_type  text        NOT NULL,
    before_hash text,
    after_hash  text,
    observed_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_thread_file_events_event_type CHECK (event_type IN ('created', 'modified', 'deleted'))
);

CREATE INDEX idx_thread_file_events_session_path
    ON session_thread_file_events (org_id, session_id, path, observed_at DESC);

CREATE INDEX idx_thread_file_events_thread
    ON session_thread_file_events (org_id, thread_id, observed_at DESC)
    WHERE thread_id IS NOT NULL;
