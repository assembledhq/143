CREATE TABLE preview_browser_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    preview_instance_id uuid REFERENCES preview_instances(id) ON DELETE SET NULL,
    context_key text NOT NULL,
    control_state text NOT NULL DEFAULT 'agent_control'
        CHECK (control_state IN ('agent_control', 'human_control', 'waiting_for_handoff')),
    control_lease_owner_id uuid REFERENCES users(id) ON DELETE SET NULL,
    control_lease_expires_at timestamptz,
    agent_action_token uuid,
    agent_action_expires_at timestamptz,
    handoff_reason text NOT NULL DEFAULT '',
    current_url text NOT NULL DEFAULT '',
    viewport jsonb NOT NULL DEFAULT '{"width":1440,"height":900}'::jsonb,
    storage_state jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (octet_length(storage_state::text) <= 1048576),
    console_cursor bigint NOT NULL DEFAULT 0,
    last_observed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_preview_browser_action_fence CHECK (
        (agent_action_token IS NULL) = (agent_action_expires_at IS NULL)
    ),
    UNIQUE (org_id, session_id),
    UNIQUE (context_key)
);

CREATE INDEX idx_preview_browser_sessions_preview
    ON preview_browser_sessions (org_id, preview_instance_id)
    WHERE preview_instance_id IS NOT NULL;
