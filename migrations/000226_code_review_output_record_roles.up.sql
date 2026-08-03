ALTER TABLE code_review_prompt_records
    DROP CONSTRAINT chk_code_review_prompt_records_role;

ALTER TABLE code_review_prompt_records
    ADD CONSTRAINT chk_code_review_prompt_records_role
    CHECK (role IN ('reviewer', 'orchestrator', 'description_policy', 'reviewer_output', 'orchestrator_output'));
