CREATE TABLE eval_release_gate_decisions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    batch_id    UUID NOT NULL REFERENCES eval_batches(id) ON DELETE CASCADE,
    gate_id     UUID NOT NULL REFERENCES eval_release_gates(id) ON DELETE CASCADE,
    status      TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'no_data')),
    reason      TEXT NOT NULL DEFAULT '',
    metrics     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, batch_id, gate_id)
);

CREATE INDEX idx_eval_release_gate_decisions_batch
    ON eval_release_gate_decisions (org_id, batch_id, created_at DESC);
