-- Code review behavior is organization-wide. Preserve repository-scoped rows
-- for reviews that already reference those historical policy versions, but
-- prevent them from becoming active again.
UPDATE code_review_policies
SET active = false
WHERE active = true
  AND repository_id IS NOT NULL;

DROP INDEX IF EXISTS idx_code_review_policies_repo_active;

ALTER TABLE code_review_policies
    ADD CONSTRAINT chk_code_review_policies_active_org_scope
    CHECK (active = false OR repository_id IS NULL);
