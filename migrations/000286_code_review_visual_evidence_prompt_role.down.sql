ALTER TABLE code_review_prompt_records
    DROP CONSTRAINT IF EXISTS chk_code_review_prompt_records_role;

-- Historical visual-evidence records remain valid audit data after a binary
-- rollback, so narrow the write contract without validating existing rows.
ALTER TABLE code_review_prompt_records
    ADD CONSTRAINT chk_code_review_prompt_records_role
    CHECK (role IN (
        'reviewer',
        'orchestrator',
        'description_policy',
        'reviewer_output',
        'orchestrator_output'
    )) NOT VALID;

DO $$
BEGIN
    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL THEN
        ALTER TABLE code_review_prompt_artifacts
            DROP CONSTRAINT IF EXISTS chk_code_review_prompt_artifacts_role;
        ALTER TABLE code_review_prompt_artifacts
            ADD CONSTRAINT chk_code_review_prompt_artifacts_role
            CHECK (role IN (
                'reviewer',
                'orchestrator',
                'description_policy',
                'reviewer_output',
                'orchestrator_output'
            )) NOT VALID;
    END IF;
END $$;
