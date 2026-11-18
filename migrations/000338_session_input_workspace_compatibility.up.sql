-- Empty passive representation, not a certificate author or readiness policy.
-- Unknown/corrupt descriptor fields stay representable; a future canonical
-- writer must validate exact immutable anchors and typed operation receipts.
-- Install the remaining hot-parent FKs in independent transactions.
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_input_workspace_compatibility (
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL,
    request_id uuid NOT NULL,
    state text NOT NULL,
    anchor_digest text,
    context_scope_digest text,
    workspace_generation bigint,
    workspace_revision bigint,
    snapshot_key text,
    workspace_state jsonb,
    last_authority_kind text,
    -- Polymorphic provenance, not an ID-only FK to a fabricated receipt ledger.
    last_authority_receipt_id uuid,
    reason text,
    next_revalidation_at timestamptz,
    CONSTRAINT session_input_workspace_compatibility_pkey
        PRIMARY KEY (org_id, request_id),
    CONSTRAINT session_input_workspace_compatibility_state_check
        CHECK (state IN ('certified', 'pending', 'unavailable', 'unprovable'))
);

CREATE INDEX idx_session_input_workspace_compatibility_session
    ON session_input_workspace_compatibility (org_id, session_id, request_id);
CREATE INDEX idx_session_input_workspace_compatibility_due
    ON session_input_workspace_compatibility (next_revalidation_at, request_id)
    WHERE state = 'pending';
