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

INSERT INTO session_threads (session_id, org_id, agent_type, model_override, label, status)
SELECT s.id, s.org_id, s.agent_type, s.model_override, 'Main', 'idle'
FROM sessions s
WHERE s.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM session_threads t WHERE t.session_id = s.id
);
