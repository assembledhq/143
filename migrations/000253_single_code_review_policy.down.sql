ALTER TABLE code_review_policies
    DROP CONSTRAINT IF EXISTS chk_code_review_policies_active_org_scope;

CREATE UNIQUE INDEX IF NOT EXISTS idx_code_review_policies_repo_active
    ON code_review_policies (org_id, repository_id)
    WHERE active = true AND repository_id IS NOT NULL;
