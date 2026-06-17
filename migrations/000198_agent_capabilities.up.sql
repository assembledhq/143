CREATE TABLE agent_capability_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    policy_type text NOT NULL,
    automation_id uuid REFERENCES automations(id) ON DELETE CASCADE,
    name text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_agent_capability_policy_type
        CHECK (policy_type IN ('session_default', 'automation')),
    CONSTRAINT chk_agent_capability_policy_owner
        CHECK (
            (policy_type = 'session_default' AND automation_id IS NULL)
            OR (policy_type = 'automation' AND automation_id IS NOT NULL)
        ),
    UNIQUE (org_id, id)
);

CREATE UNIQUE INDEX idx_agent_capability_policies_session_default
    ON agent_capability_policies (org_id)
    WHERE policy_type = 'session_default' AND active = true;

CREATE UNIQUE INDEX idx_agent_capability_policies_automation
    ON agent_capability_policies (org_id, automation_id)
    WHERE policy_type = 'automation' AND active = true;

CREATE TABLE agent_capability_policy_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    policy_id uuid NOT NULL,
    capability_id text NOT NULL,
    access_level text NOT NULL DEFAULT 'read',
    enabled boolean NOT NULL DEFAULT true,
    config jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_agent_capability_grant_access_level
        CHECK (access_level IN ('read', 'write', 'publish')),
    CONSTRAINT chk_agent_capability_grant_config_object
        CHECK (jsonb_typeof(config) = 'object'),
    CONSTRAINT fk_agent_capability_grants_policy
        FOREIGN KEY (org_id, policy_id)
        REFERENCES agent_capability_policies (org_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_agent_capability_policy_grants_unique
    ON agent_capability_policy_grants (org_id, policy_id, capability_id);

ALTER TABLE sessions
    ADD COLUMN capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_sessions_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array');

ALTER TABLE automation_runs
    ADD COLUMN capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT chk_automation_runs_capability_snapshot_array
        CHECK (jsonb_typeof(capability_snapshot) = 'array');
