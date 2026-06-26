WITH default_capabilities(capability_id, access_level) AS (
    VALUES
        ('issue_sources', 'read'),
        ('production_diagnostics', 'read'),
        ('slack_notifications', 'write')
),
active_defaults AS (
    SELECT id, org_id
    FROM agent_capability_policies
    WHERE policy_type = 'session_default'
      AND active = true
)
INSERT INTO agent_capability_policy_grants (
    org_id,
    policy_id,
    capability_id,
    access_level,
    enabled,
    config
)
SELECT
    p.org_id,
    p.id,
    c.capability_id,
    c.access_level,
    true,
    '{}'::jsonb
FROM active_defaults p
CROSS JOIN default_capabilities c
WHERE true
ON CONFLICT (org_id, policy_id, capability_id) DO UPDATE
SET enabled = true,
    access_level = EXCLUDED.access_level;
