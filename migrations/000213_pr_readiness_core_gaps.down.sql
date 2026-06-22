DROP TABLE IF EXISTS pr_readiness_contexts;
DROP TABLE IF EXISTS pr_readiness_bypasses;
DROP TABLE IF EXISTS pr_readiness_custom_checks;
DROP TABLE IF EXISTS pr_readiness_policies;

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_enforcement_builder,
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_enforcement_engineer,
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_enforcement_admin,
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_provenance,
    DROP COLUMN IF EXISTS check_key,
    DROP COLUMN IF EXISTS enforcement_builder,
    DROP COLUMN IF EXISTS enforcement_engineer,
    DROP COLUMN IF EXISTS enforcement_admin,
    DROP COLUMN IF EXISTS provenance,
    DROP COLUMN IF EXISTS source;

-- Restore the original 000209 constraints (auto-generated names, narrower value sets).
ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_status,
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_check_type,
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_checks_enforcement;

ALTER TABLE pr_readiness_checks
    ADD CONSTRAINT pr_readiness_checks_status_check
        CHECK (status IN ('passed', 'warning', 'failed', 'skipped')),
    ADD CONSTRAINT pr_readiness_checks_check_type_check
        CHECK (check_type IN (
            'freshness',
            'agent_review_clean',
            'diff_collected',
            'test_evidence_present',
            'risk_flags',
            'dependency_config_risk',
            'generated_file_churn',
            'context_complete',
            'review_packet_draftable'
        )),
    ADD CONSTRAINT pr_readiness_checks_enforcement_check
        CHECK (enforcement IN ('off', 'advisory', 'blocking'));

ALTER TABLE pr_readiness_runs
    DROP CONSTRAINT IF EXISTS chk_pr_readiness_runs_status,
    ADD CONSTRAINT pr_readiness_runs_status_check
        CHECK (status IN ('queued', 'running', 'passed', 'warnings', 'blocked', 'failed'));
