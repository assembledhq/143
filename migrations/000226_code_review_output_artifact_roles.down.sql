ALTER TABLE code_review_prompt_artifacts
    DROP CONSTRAINT chk_code_review_prompt_artifacts_role;

ALTER TABLE code_review_prompt_artifacts
    ADD CONSTRAINT chk_code_review_prompt_artifacts_role
    CHECK (role IN ('reviewer', 'orchestrator', 'description_policy'));
