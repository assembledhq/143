ALTER TABLE code_review_requests DROP CONSTRAINT code_review_requests_mode_check;
ALTER TABLE code_review_requests ADD CONSTRAINT code_review_requests_mode_check
    CHECK (mode IN ('ensure_current','review_now','recheck','force_fresh'));

CREATE UNIQUE INDEX code_review_requests_org_id_id ON code_review_requests(org_id,id);

ALTER TABLE code_review_pr_state
    ADD COLUMN active_assessment_id uuid,
    ADD COLUMN current_assessment_id uuid;
ALTER TABLE code_review_pr_state ADD CONSTRAINT code_review_pr_active_assessment_fk
    FOREIGN KEY (org_id,pull_request_id,active_assessment_id)
    REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
ALTER TABLE code_review_pr_state ADD CONSTRAINT code_review_pr_current_assessment_fk
    FOREIGN KEY (org_id,pull_request_id,current_assessment_id)
    REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);

ALTER TABLE code_review_requests
    ADD COLUMN assessment_id uuid,
    ADD COLUMN retry_of_assessment_id uuid;
ALTER TABLE code_review_requests ADD CONSTRAINT code_review_request_assessment_fk
    FOREIGN KEY (org_id,pull_request_id,assessment_id)
    REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
ALTER TABLE code_review_requests ADD CONSTRAINT code_review_request_retry_assessment_fk
    FOREIGN KEY (org_id,pull_request_id,retry_of_assessment_id)
    REFERENCES code_review_revision_assessments(org_id,pull_request_id,id);
CREATE INDEX code_review_requests_assessment ON code_review_requests(org_id,assessment_id) WHERE assessment_id IS NOT NULL;
