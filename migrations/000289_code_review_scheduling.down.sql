ALTER TABLE code_review_pr_state DROP CONSTRAINT code_review_pr_pending_request_fk;
DROP TABLE code_review_requests;
DROP TABLE code_review_pr_state;
ALTER TABLE code_review_policies DROP COLUMN scheduling_policy;
