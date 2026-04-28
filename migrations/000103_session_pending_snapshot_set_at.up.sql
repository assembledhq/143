-- Records when pending_snapshot_key was set, so a periodic reaper can
-- identify and clear rows whose owning upload goroutine died (e.g. worker
-- OOM'd before Promote/Clear could fire). Without this column, a stranded
-- pending_snapshot_key would block continue_session forever via the
-- orchestrator's gate, and CreatePR retries don't recover it.
--
-- Cleared (NULL) by Promote and Clear in lockstep with pending_snapshot_key,
-- so the column is meaningful only while an upload is believed to be
-- in-flight. The reaper considers any (pending_snapshot_key IS NOT NULL,
-- pending_snapshot_set_at < now() - threshold) row stranded.
ALTER TABLE sessions
    ADD COLUMN pending_snapshot_set_at TIMESTAMPTZ;

-- Partial index keeps the reaper scan cheap: only sessions with an active
-- pending snapshot are in scope, which is normally a tiny fraction of the
-- table (sessions in pr_created state for the few minutes an upload runs).
CREATE INDEX IF NOT EXISTS idx_sessions_pending_snapshot_set_at
    ON sessions (pending_snapshot_set_at)
    WHERE pending_snapshot_key IS NOT NULL;
