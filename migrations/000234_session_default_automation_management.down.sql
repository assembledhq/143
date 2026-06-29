UPDATE agent_capability_policy_grants g
SET enabled = false
FROM agent_capability_policies p
WHERE g.org_id = p.org_id
  AND g.policy_id = p.id
  AND p.policy_type = 'session_default'
  AND p.active = true
  AND g.capability_id = 'automation_management';
