ALTER TABLE pull_request_repair_runs
    ADD COLUMN auto_attempt BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN trigger_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN triggered_by_source TEXT NOT NULL DEFAULT 'manual',
    ADD COLUMN triggered_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_pull_request_repair_runs_auto_attempts
    ON pull_request_repair_runs (org_id, pull_request_id, head_sha, action_type)
    WHERE auto_attempt = true;
