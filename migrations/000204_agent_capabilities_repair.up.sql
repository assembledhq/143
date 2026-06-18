CREATE TABLE IF NOT EXISTS agent_capability_policies (
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_capability_policies_session_default
    ON agent_capability_policies (org_id)
    WHERE policy_type = 'session_default' AND active = true;

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_capability_policies_automation
    ON agent_capability_policies (org_id, automation_id)
    WHERE policy_type = 'automation' AND active = true;

CREATE TABLE IF NOT EXISTS agent_capability_policy_grants (
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

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_capability_policy_grants_unique
    ON agent_capability_policy_grants (org_id, policy_id, capability_id);

ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb;

UPDATE sessions
SET capability_snapshot = '[]'::jsonb
WHERE capability_snapshot IS NULL;

ALTER TABLE sessions
    ALTER COLUMN capability_snapshot SET DEFAULT '[]'::jsonb,
    ALTER COLUMN capability_snapshot SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'sessions'::regclass
          AND conname = 'chk_sessions_capability_snapshot_array'
    ) THEN
        ALTER TABLE sessions
            ADD CONSTRAINT chk_sessions_capability_snapshot_array
            CHECK (jsonb_typeof(capability_snapshot) = 'array') NOT VALID;
    END IF;
END $$;

ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_capability_snapshot_array;

ALTER TABLE automation_runs
    ADD COLUMN IF NOT EXISTS capability_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb;

UPDATE automation_runs
SET capability_snapshot = '[]'::jsonb
WHERE capability_snapshot IS NULL;

ALTER TABLE automation_runs
    ALTER COLUMN capability_snapshot SET DEFAULT '[]'::jsonb,
    ALTER COLUMN capability_snapshot SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'automation_runs'::regclass
          AND conname = 'chk_automation_runs_capability_snapshot_array'
    ) THEN
        ALTER TABLE automation_runs
            ADD CONSTRAINT chk_automation_runs_capability_snapshot_array
            CHECK (jsonb_typeof(capability_snapshot) = 'array') NOT VALID;
    END IF;
END $$;

ALTER TABLE automation_runs VALIDATE CONSTRAINT chk_automation_runs_capability_snapshot_array;
