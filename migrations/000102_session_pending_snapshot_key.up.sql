-- Tracks an in-flight snapshot upload for a session. When non-NULL, a worker
-- has captured a tar of the post-PR sandbox locally and is asynchronously
-- uploading it to object storage; once Save() completes the column is
-- promoted into snapshot_key (and pending_snapshot_key cleared) atomically
-- via PromotePendingSnapshot. Hydration paths must wait until this column is
-- NULL before resuming a session — otherwise they would restore the stale
-- pre-PR snapshot (uncommitted edits at the original BaseCommitSHA) instead
-- of the post-push state agents and "Fix tests" actually need.
--
-- Why a separate column instead of overwriting snapshot_key directly: the
-- upload can fail or the worker can die mid-upload. Keeping snapshot_key
-- pointing at the last-known-good blob until the new one lands ensures we
-- never advertise a key whose blob doesn't exist in storage.
ALTER TABLE sessions
    ADD COLUMN pending_snapshot_key TEXT;
