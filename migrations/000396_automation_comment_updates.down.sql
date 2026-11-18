-- Preserve durable update receipts rather than silently discarding them on rollback.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM automation_actions WHERE kind='github_issue_comment_update') THEN
  RAISE EXCEPTION 'cannot remove comment-update support while update receipts exist';
 END IF;
END $$;
DROP INDEX idx_automation_actions_comment_updates;
ALTER TABLE automation_actions DROP CONSTRAINT automation_actions_kind_check;
ALTER TABLE automation_actions ADD CONSTRAINT automation_actions_kind_check
 CHECK (kind IN ('github_label','github_team_review','github_issue_comment','notion_tracking_row','slack_notification'));
