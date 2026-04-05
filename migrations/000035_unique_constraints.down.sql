ALTER TABLE project_cycles DROP CONSTRAINT IF EXISTS uq_project_cycles_project_cycle;
ALTER TABLE pull_requests DROP CONSTRAINT IF EXISTS uq_pull_requests_github;
