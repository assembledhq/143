DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM automation_actions WHERE kind='github_pull_request_review') THEN
  RAISE EXCEPTION 'cannot remove inline review support while review receipts exist';
 END IF;
 IF EXISTS (
  SELECT 1
  FROM agent_capability_policy_grants g
  JOIN agent_capability_policies p ON p.org_id=g.org_id AND p.id=g.policy_id
  JOIN automations a ON a.org_id=p.org_id AND a.id=p.automation_id
  WHERE p.policy_type='automation' AND p.active AND a.deleted_at IS NULL
   AND g.capability_id='automation_actions' AND g.enabled
   AND g.config @> '{"actions":["github_pull_request_review"]}'::jsonb
 ) THEN
  RAISE EXCEPTION 'cannot remove inline review support while enabled inline review grants exist';
 END IF;
END $$;
ALTER TABLE automation_actions DROP CONSTRAINT automation_actions_kind_check;
ALTER TABLE automation_actions ADD CONSTRAINT automation_actions_kind_check
 CHECK (kind IN ('github_label','github_team_review','github_issue_comment','github_issue_comment_update','notion_tracking_row','slack_notification'));
