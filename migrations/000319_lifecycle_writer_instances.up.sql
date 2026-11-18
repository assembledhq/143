-- Passive, partial process observations only. An unexpired row is not evidence
-- that every writer is registered, compatible, or finished with in-flight work.
SET LOCAL lock_timeout = '5s';

CREATE TABLE lifecycle_writer_instances ( -- lint:no-org-id reason="global passive process observations without tenant, host or PID inventory"
    instance_id uuid PRIMARY KEY
        CONSTRAINT lifecycle_writer_instances_nonzero_id CHECK (instance_id <> '00000000-0000-0000-0000-000000000000'::uuid),
    role text NOT NULL
        CONSTRAINT lifecycle_writer_instances_role_bounds CHECK (btrim(role) <> '' AND octet_length(role) <= 64),
    build_sha text NOT NULL
        CONSTRAINT lifecycle_writer_instances_build_bounds CHECK (btrim(build_sha) <> '' AND octet_length(build_sha) <= 128),
    lifecycle_writer_version integer NOT NULL DEFAULT 0
        CONSTRAINT lifecycle_writer_instances_version_nonnegative CHECK (lifecycle_writer_version >= 0),
    last_heartbeat timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CONSTRAINT lifecycle_writer_instances_positive_ttl CHECK (expires_at > last_heartbeat)
);

CREATE INDEX lifecycle_writer_instances_expiry_idx
    ON lifecycle_writer_instances (expires_at, instance_id);
