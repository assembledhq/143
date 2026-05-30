ALTER TABLE pull_requests
    ADD COLUMN merge_when_ready_state text NOT NULL DEFAULT 'off',
    ADD COLUMN merge_when_ready_requested_by uuid NULL REFERENCES users(id),
    ADD COLUMN merge_when_ready_requested_at timestamptz,
    ADD COLUMN merge_when_ready_head_sha text NOT NULL DEFAULT '',
    ADD COLUMN merge_when_ready_health_version bigint,
    ADD COLUMN merge_when_ready_error text NOT NULL DEFAULT '',
    ADD COLUMN merge_when_ready_updated_at timestamptz;

ALTER TABLE pull_requests
    ADD CONSTRAINT chk_pull_requests_merge_when_ready_state CHECK (merge_when_ready_state IN (
        'off', 'queued', 'merging', 'succeeded', 'failed', 'cancelled'
    )) NOT VALID;
ALTER TABLE pull_requests VALIDATE CONSTRAINT chk_pull_requests_merge_when_ready_state;

CREATE INDEX idx_pull_requests_merge_when_ready
    ON pull_requests (org_id, merge_when_ready_state, updated_at)
    WHERE merge_when_ready_state IN ('queued', 'merging');
