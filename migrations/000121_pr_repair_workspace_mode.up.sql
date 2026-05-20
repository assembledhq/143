ALTER TABLE pull_request_repair_runs
    ADD COLUMN workspace_mode TEXT NOT NULL DEFAULT 'snapshot_continuation';

