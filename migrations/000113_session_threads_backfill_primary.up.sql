-- Backfill a primary thread row for every session that has none.
--
-- PR #724 introduced multi-tab concurrency, but the AgentTabStrip in the
-- session detail page renders nothing when a session has zero threads, and
-- nothing in the create path was seeding a default row. Result: every
-- pre-existing session — and every new session before the matching app
-- change — was invisible to the multi-tab UI (no tabs, no "+" button, no
-- fork menu). This backfill establishes the invariant that every session
-- has at least one thread; the SessionStore.Create change keeps it true
-- going forward.
--
-- The seeded row mirrors the session's agent_type and model_override so the
-- "primary" tab matches the agent the session was started under. Status is
-- 'idle' to match migration 108's default for never-run threads.
--
-- Rolling-deploy note: during the window where this migration has run but
-- the matching app change has not, an old app pod can still create a
-- thread-less session. That degrades gracefully — the worker run_agent
-- handler falls back to ListBySession() and runs the session with NULL
-- thread attribution (the pre-PR behaviour). New pods enforce the
-- invariant; any leftover thread-less sessions can be cleaned up by
-- re-running the INSERT below after the deploy settles.

INSERT INTO session_threads (session_id, org_id, agent_type, model_override, label, status, current_turn, last_activity_at, started_at, completed_at)
SELECT
  s.id,
  s.org_id,
  s.agent_type,
  s.model_override,
  'Main',
  CASE
    WHEN s.status = 'pending' THEN 'pending'
    WHEN s.status = 'running' THEN 'running'
    WHEN s.status = 'awaiting_input' THEN 'awaiting_input'
    WHEN s.status = 'failed' THEN 'failed'
    WHEN s.status = 'cancelled' THEN 'cancelled'
    WHEN s.status IN ('completed', 'pr_created') THEN 'completed'
    ELSE 'idle'
  END,
  s.current_turn,
  s.last_activity_at,
  s.started_at,
  s.completed_at
FROM sessions s
WHERE s.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM session_threads t WHERE t.session_id = s.id
);

-- Existing first-turn transcripts and logs predate thread attribution and
-- have NULL thread_id. Once the primary tab exists, the UI fetches the
-- selected tab's thread-scoped timeline, so assign unthreaded history to the
-- single primary thread where that mapping is unambiguous.
UPDATE session_messages m
SET thread_id = t.id
FROM session_threads t
WHERE m.org_id = t.org_id
  AND m.session_id = t.session_id
  AND m.thread_id IS NULL
  AND t.label = 'Main'
  AND (SELECT count(*) FROM session_threads x WHERE x.org_id = t.org_id AND x.session_id = t.session_id) = 1;

UPDATE session_logs l
SET thread_id = t.id
FROM session_threads t
WHERE l.org_id = t.org_id
  AND l.session_id = t.session_id
  AND l.thread_id IS NULL
  AND t.label = 'Main'
  AND (SELECT count(*) FROM session_threads x WHERE x.org_id = t.org_id AND x.session_id = t.session_id) = 1;
