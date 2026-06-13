ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS chk_preview_instances_unavailable_reason;

ALTER TABLE preview_instances
    ADD CONSTRAINT chk_preview_instances_unavailable_reason CHECK (
        unavailable_reason IN ('', 'owner_lost', 'deploy_drain_timeout', 'host_maintenance', 'emergency_force', 'lease_expired')
    );

ALTER TABLE preview_runtimes
    DROP CONSTRAINT IF EXISTS chk_preview_runtimes_unavailable_reason;

ALTER TABLE preview_runtimes
    ADD CONSTRAINT chk_preview_runtimes_unavailable_reason CHECK (
        unavailable_reason IN ('', 'owner_lost', 'deploy_drain_timeout', 'host_maintenance', 'emergency_force', 'lease_expired')
    );
