ALTER TABLE automation_actions DROP CONSTRAINT automation_actions_kind_check;
ALTER TABLE automation_actions ADD CONSTRAINT automation_actions_kind_check
 CHECK (kind IN ('github_label','github_team_review','github_issue_comment','github_issue_comment_update','notion_tracking_row','slack_notification'));

CREATE INDEX idx_automation_actions_comment_updates
 ON automation_actions (org_id,automation_id,repository_id,scope_key,(payload->'request'->>'source_action_id'))
 WHERE kind='github_issue_comment_update';
