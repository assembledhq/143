ALTER TABLE preview_instances
    ADD COLUMN peak_memory_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN peak_memory_sampled_at timestamptz,
    ADD COLUMN peak_memory_phase text NOT NULL DEFAULT '';

CREATE TABLE preview_resource_samples (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    preview_instance_id uuid NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    worker_node_id text NOT NULL DEFAULT '',
    phase text NOT NULL DEFAULT '',
    memory_bytes bigint NOT NULL DEFAULT 0,
    memory_limit_bytes bigint NOT NULL DEFAULT 0,
    cpu_cores double precision NOT NULL DEFAULT 0,
    cpu_limit_millis int NOT NULL DEFAULT 0,
    processes jsonb NOT NULL DEFAULT '[]'::jsonb,
    sampled_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_preview_resource_samples_preview
    ON preview_resource_samples (org_id, preview_instance_id, sampled_at DESC);
