-- Widens whichever table this installation carries: a fresh database has the
-- records table migration 225 created, while a database that was expanded and
-- then contracted by migration 275 carries the legacy artifacts table. This
-- mirrors the rollback so a legacy installation can be migrated down past this
-- version and replayed forward without hitting a missing relation.
DO $$
BEGIN
    IF to_regclass('code_review_prompt_records') IS NOT NULL THEN
        ALTER TABLE code_review_prompt_records
            DROP CONSTRAINT IF EXISTS chk_code_review_prompt_records_role;
        ALTER TABLE code_review_prompt_records
            ADD CONSTRAINT chk_code_review_prompt_records_role
            CHECK (role IN ('reviewer', 'orchestrator', 'description_policy', 'reviewer_output', 'orchestrator_output'));
    END IF;

    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL THEN
        ALTER TABLE code_review_prompt_artifacts
            DROP CONSTRAINT IF EXISTS chk_code_review_prompt_artifacts_role;
        ALTER TABLE code_review_prompt_artifacts
            ADD CONSTRAINT chk_code_review_prompt_artifacts_role
            CHECK (role IN ('reviewer', 'orchestrator', 'description_policy', 'reviewer_output', 'orchestrator_output'));
    END IF;
END $$;
