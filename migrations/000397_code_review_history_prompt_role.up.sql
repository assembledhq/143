SET LOCAL lock_timeout = '5s';

ALTER TABLE code_review_prompt_records
    DROP CONSTRAINT IF EXISTS chk_code_review_prompt_records_role;

ALTER TABLE code_review_prompt_records
    ADD CONSTRAINT chk_code_review_prompt_records_role
    CHECK (role IN (
        'reviewer',
        'orchestrator',
        'description_policy',
        'reviewer_output',
        'orchestrator_output',
        'visual_evidence',
        'review_history'
    ));

-- The draining legacy generation synchronizes prompt writes through a trigger.
-- Its role contract must accept the same immutable retrieval receipts.
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
                'orchestrator_output',
                'visual_evidence',
                'review_history'
            ));
    END IF;
END $$;
