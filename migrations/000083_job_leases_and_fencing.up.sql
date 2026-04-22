ALTER TABLE jobs
    ADD COLUMN lease_expires_at timestamptz,
    ADD COLUMN lock_token uuid,
    ADD COLUMN run_owner_id text;

CREATE INDEX idx_jobs_reclaimable
    ON jobs (status, lease_expires_at)
    WHERE status = 'running';

CREATE INDEX idx_jobs_running_by_node
    ON jobs (locked_by_node_id, status)
    WHERE locked_by_node_id IS NOT NULL;
