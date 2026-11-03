SET LOCAL lock_timeout = '5s';
-- The old validated check still forbids parked rows until migration 352.
ALTER TABLE jobs RENAME CONSTRAINT chk_jobs_status TO chk_jobs_status_legacy;
ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_status CHECK (status IN (
        'pending', 'running', 'awaiting_start_control',
        'succeeded', 'failed', 'cancelled', 'dead_letter'
    )) NOT VALID,
    ADD CONSTRAINT chk_jobs_parked_without_lease CHECK (
        status <> 'awaiting_start_control' OR (
            locked_by_node_id IS NULL AND run_owner_id IS NULL
            AND lock_token IS NULL AND locked_at IS NULL
            AND lease_expires_at IS NULL AND completed_at IS NULL
        )
    ) NOT VALID;
