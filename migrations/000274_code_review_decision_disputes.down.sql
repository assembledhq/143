ALTER TABLE pull_requests
    DROP CONSTRAINT IF EXISTS chk_pr_code_review_dispute_cycles,
    DROP COLUMN IF EXISTS code_review_dispute_cycles_in_epoch,
    DROP COLUMN IF EXISTS code_review_dispute_epoch;

ALTER TABLE code_review_session_metadata
    DROP CONSTRAINT IF EXISTS fk_code_review_metadata_triggering_dispute_org,
    DROP COLUMN IF EXISTS triggering_dispute_id,
    DROP CONSTRAINT chk_code_review_session_metadata_trigger_source,
    ADD CONSTRAINT chk_code_review_session_metadata_trigger_source
        CHECK (trigger_source IN ('app_reviewer', 'alias_reviewer', 'team_reviewer',
                                  'slash_command', 'auto_policy'));

DROP TABLE IF EXISTS code_review_reassessment_admissions;
DROP TABLE IF EXISTS code_review_dispute_escalations;
DROP TABLE IF EXISTS code_review_dispute_authorizations;
DROP TABLE IF EXISTS code_review_decision_disputes;

DROP INDEX IF EXISTS idx_code_review_policies_org_id_id_for_disputes;
DROP INDEX IF EXISTS idx_repositories_org_id_id_for_code_review_disputes;
DROP INDEX IF EXISTS idx_pull_requests_org_id_id_for_code_review_disputes;
DROP INDEX IF EXISTS idx_sessions_org_id_id_for_code_review_disputes;
