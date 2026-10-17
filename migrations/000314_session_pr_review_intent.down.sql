DROP TABLE IF EXISTS publication_team_review_requests;
DROP TABLE IF EXISTS session_pr_intents;
ALTER TABLE session_publications DROP CONSTRAINT IF EXISTS session_publications_id_org_id_key;
ALTER TABLE session_automation_links DROP CONSTRAINT IF EXISTS session_automation_links_run_scope_key;
ALTER TABLE automation_runs DROP CONSTRAINT IF EXISTS automation_runs_id_org_id_key;
