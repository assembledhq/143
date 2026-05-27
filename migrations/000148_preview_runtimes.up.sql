-- Explicit live worker attachments for durable preview instances.
CREATE TABLE preview_runtimes (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    runtime_epoch       INT         NOT NULL,
    worker_node_id      TEXT        NOT NULL DEFAULT '',
    endpoint_url        TEXT        NOT NULL DEFAULT '',
    preview_handle      TEXT        NOT NULL DEFAULT '',
    primary_port        INT         NOT NULL DEFAULT 0,
    status              TEXT        NOT NULL DEFAULT 'starting',
    lease_expires_at    TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '90 seconds',
    last_heartbeat_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    drain_requested_at  TIMESTAMPTZ,
    stopped_at          TIMESTAMPTZ,
    error               TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (preview_instance_id, runtime_epoch)
);

CREATE INDEX idx_preview_runtimes_preview_active
    ON preview_runtimes (org_id, preview_instance_id, runtime_epoch DESC)
    WHERE status IN ('starting', 'ready', 'draining');

CREATE INDEX idx_preview_runtimes_worker_status
    ON preview_runtimes (worker_node_id, status, lease_expires_at);

CREATE INDEX idx_preview_runtimes_endpoint_active
    ON preview_runtimes (endpoint_url, status);

INSERT INTO preview_runtimes (
    org_id,
    preview_instance_id,
    runtime_epoch,
    worker_node_id,
    endpoint_url,
    preview_handle,
    primary_port,
    status,
    lease_expires_at,
    last_heartbeat_at,
    created_at,
    updated_at
)
SELECT
    pi.org_id,
    pi.id,
    1,
    pi.worker_node_id,
    COALESCE(NULLIF(n.metadata->>'preview_internal_base_url', ''), ''),
    pi.preview_handle,
    pi.port,
    CASE
        WHEN pi.status = 'starting' THEN 'starting'
        WHEN pi.status IN ('ready', 'partially_ready', 'unhealthy') THEN 'ready'
        ELSE 'failed'
    END,
    now() + INTERVAL '90 seconds',
    now(),
    now(),
    now()
FROM preview_instances pi
LEFT JOIN nodes n ON n.id = pi.worker_node_id
WHERE pi.status IN ('starting', 'ready', 'partially_ready', 'unhealthy');
