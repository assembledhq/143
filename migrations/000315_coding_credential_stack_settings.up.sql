-- Per-scope selection mode for a coding-credential stack. "ordered" keeps the
-- priority-tier walk; "spread" collapses every runnable credential in the scope
-- into one tier so the picker distributes work across all of them instead of
-- draining the top one first. One row per scope; no row means "ordered".
CREATE TABLE coding_credential_stack_settings (
    org_id uuid NOT NULL REFERENCES organizations(id),
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    distribution text NOT NULL DEFAULT 'ordered',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT coding_credential_stack_settings_scope_key
        UNIQUE NULLS NOT DISTINCT (org_id, user_id),
    CONSTRAINT chk_coding_credential_stack_settings_distribution
        CHECK (distribution IN ('ordered', 'spread'))
);
