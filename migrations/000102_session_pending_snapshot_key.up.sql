-- Tracks an in-flight snapshot upload for a session. When pending_snapshot_key
-- is non-NULL, a worker has captured a tar of the post-PR sandbox locally and
-- is asynchronously uploading it to object storage; once Save() completes the
-- column is promoted into snapshot_key (and pending_snapshot_key cleared)
-- atomically via PromotePendingSnapshot. Hydration paths must wait until this
-- column is NULL before resuming a session — otherwise they would restore the
-- stale pre-PR snapshot (uncommitted edits at the original BaseCommitSHA)
-- instead of the post-push state agents and "Fix tests" actually need.
--
-- Why a separate column instead of overwriting snapshot_key directly: the
-- upload can fail or the worker can die mid-upload. Keeping snapshot_key
-- pointing at the last-known-good blob until the new one lands ensures we
-- never advertise a key whose blob doesn't exist in storage.
--
-- pending_snapshot_set_at records when pending_snapshot_key was last set so a
-- periodic reaper can identify and clear rows whose owning upload goroutine
-- died (e.g. worker OOM'd before Promote/Clear could fire). Without it, a
-- stranded pending_snapshot_key would block continue_session forever via the
-- orchestrator's gate, and CreatePR retries don't recover it. Cleared (NULL)
-- by Promote and Clear in lockstep with pending_snapshot_key, so the column
-- is meaningful only while an upload is believed to be in-flight. The reaper
-- considers any (pending_snapshot_key IS NOT NULL,
-- pending_snapshot_set_at < now() - threshold) row stranded.
--
-- Both columns are added in a single ALTER TABLE so rollout takes one brief
-- exclusive lock on `sessions` instead of two.
ALTER TABLE sessions
    ADD COLUMN pending_snapshot_key TEXT,
    ADD COLUMN pending_snapshot_set_at TIMESTAMPTZ;

-- Partial index keeps the reaper scan cheap: only sessions with an active
-- pending snapshot are in scope, which is normally a tiny fraction of the
-- table (sessions in pr_created state for the few minutes an upload runs).
CREATE INDEX IF NOT EXISTS idx_sessions_pending_snapshot_set_at
    ON sessions (pending_snapshot_set_at)
    WHERE pending_snapshot_key IS NOT NULL;
