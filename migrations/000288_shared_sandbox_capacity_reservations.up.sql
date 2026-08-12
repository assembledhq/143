-- Final sandbox admission runs in both the main worker process and isolated
-- session-executor processes. Persist short-lived admission leases so those
-- processes serialize against the same worker capacity instead of relying on
-- process-local counters alone.
CREATE TABLE sandbox_capacity_reservations ( -- lint:no-org-id reason="ephemeral fleet capacity leases are worker-scoped and may represent work from any organization"
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Runtime leases intentionally avoid parent FKs: creating them during a
    -- rolling deploy would lock the hot nodes/jobs tables, while orphaned
    -- leases are harmless after expires_at and are cleaned on admission.
    node_id text NOT NULL,
    job_id uuid,
    workload_class text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_sandbox_capacity_reservations_workload_class
        CHECK (workload_class IN ('interactive', 'code_review'))
);

CREATE UNIQUE INDEX idx_sandbox_capacity_reservations_job
    ON sandbox_capacity_reservations (job_id)
    WHERE job_id IS NOT NULL;

CREATE INDEX idx_sandbox_capacity_reservations_node_expiry
    ON sandbox_capacity_reservations (node_id, expires_at);
