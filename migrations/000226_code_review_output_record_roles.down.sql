-- Narrows whichever table this installation carries: a fresh database has the
-- records table, while a database that was expanded and then contracted by
-- migration 275 carries the legacy artifacts table.
--
-- NOT VALID is required, not cosmetic: the up widened this check precisely
-- because the application writes the reviewer_output and orchestrator_output
-- roles, so any database with review history holds rows the narrowed check
-- rejects. Validating them would abort the rollback. New writes are still
-- constrained, and migration 225's rollback drops the table on the next step.
DO $$
BEGIN
    IF to_regclass('code_review_prompt_records') IS NOT NULL THEN
        ALTER TABLE code_review_prompt_records
            DROP CONSTRAINT IF EXISTS chk_code_review_prompt_records_role;
        ALTER TABLE code_review_prompt_records
            ADD CONSTRAINT chk_code_review_prompt_records_role
            CHECK (role IN ('reviewer', 'orchestrator', 'description_policy')) NOT VALID;
    END IF;

    IF to_regclass('code_review_prompt_artifacts') IS NOT NULL THEN
        ALTER TABLE code_review_prompt_artifacts
            DROP CONSTRAINT IF EXISTS chk_code_review_prompt_artifacts_role;
        ALTER TABLE code_review_prompt_artifacts
            ADD CONSTRAINT chk_code_review_prompt_artifacts_role
            CHECK (role IN ('reviewer', 'orchestrator', 'description_policy')) NOT VALID;
    END IF;
END $$;
