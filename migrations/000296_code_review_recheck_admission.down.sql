DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM code_review_requests WHERE mode IN ('recheck','force_fresh')) THEN
        RAISE EXCEPTION 'cannot roll back recheck request modes while rows remain';
    END IF;
END $$;

DROP INDEX code_review_requests_assessment;
ALTER TABLE code_review_requests DROP CONSTRAINT code_review_request_retry_assessment_fk;
ALTER TABLE code_review_requests DROP CONSTRAINT code_review_request_assessment_fk;
ALTER TABLE code_review_requests DROP COLUMN retry_of_assessment_id, DROP COLUMN assessment_id;
ALTER TABLE code_review_pr_state DROP CONSTRAINT code_review_pr_current_assessment_fk;
ALTER TABLE code_review_pr_state DROP CONSTRAINT code_review_pr_active_assessment_fk;
ALTER TABLE code_review_pr_state DROP COLUMN current_assessment_id, DROP COLUMN active_assessment_id;
DROP INDEX code_review_requests_org_id_id;
ALTER TABLE code_review_requests DROP CONSTRAINT code_review_requests_mode_check;
ALTER TABLE code_review_requests ADD CONSTRAINT code_review_requests_mode_check
    CHECK (mode IN ('ensure_current','review_now'));
