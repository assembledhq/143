DROP TABLE IF EXISTS pr_readiness_contexts;
DROP TABLE IF EXISTS pr_readiness_bypasses;
DROP TABLE IF EXISTS pr_readiness_custom_checks;
DROP TABLE IF EXISTS pr_readiness_policies;

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_status_check;

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_check_type_check;

ALTER TABLE pr_readiness_checks
    ADD CONSTRAINT pr_readiness_checks_status_check
    CHECK (status IN ('passed', 'warning', 'failed', 'skipped'));

ALTER TABLE pr_readiness_checks
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
    ));

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_builder_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_engineer_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_admin_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_provenance_check,
    DROP COLUMN IF EXISTS check_key,
    DROP COLUMN IF EXISTS enforcement_builder,
    DROP COLUMN IF EXISTS enforcement_engineer,
    DROP COLUMN IF EXISTS enforcement_admin,
    DROP COLUMN IF EXISTS provenance,
    DROP COLUMN IF EXISTS source;
