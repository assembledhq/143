ALTER TABLE automation_actions DROP CONSTRAINT automation_actions_kind_check;
ALTER TABLE automation_actions ADD CONSTRAINT automation_actions_kind_check
 CHECK (kind IN ('github_label','github_team_review','github_issue_comment','github_issue_comment_update','github_pull_request_review','notion_tracking_row','slack_notification'));
