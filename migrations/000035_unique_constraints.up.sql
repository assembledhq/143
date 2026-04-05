-- Add missing composite unique constraints to prevent duplicate logical records.
--
-- NOTE: Adding unique constraints acquires ACCESS EXCLUSIVE lock and validates
-- all existing rows. For large tables, consider running during low-traffic
-- periods. If duplicates exist, they must be resolved first.

-- Remove any duplicate cycle numbers before adding constraint.
-- Strategy: keep the most recently updated row and delete the older duplicate.
DELETE FROM project_cycles a
USING project_cycles b
WHERE a.id != b.id
  AND a.project_id = b.project_id
  AND a.cycle_number = b.cycle_number
  AND (a.updated_at < b.updated_at OR (a.updated_at = b.updated_at AND a.id > b.id));

-- Prevent duplicate cycle numbers within a project.
ALTER TABLE project_cycles
    ADD CONSTRAINT uq_project_cycles_project_cycle UNIQUE (project_id, cycle_number);

-- Remove any duplicate GitHub PRs before adding constraint.
-- Strategy: keep the most recently updated row and delete the older duplicate.
DELETE FROM pull_requests a
USING pull_requests b
WHERE a.id != b.id
  AND a.org_id = b.org_id
  AND a.github_repo = b.github_repo
  AND a.github_pr_number = b.github_pr_number
  AND (a.updated_at < b.updated_at OR (a.updated_at = b.updated_at AND a.id > b.id));

-- Prevent duplicate GitHub PRs from being recorded.
ALTER TABLE pull_requests
    ADD CONSTRAINT uq_pull_requests_github UNIQUE (org_id, github_repo, github_pr_number);
