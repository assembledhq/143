UPDATE code_review_pr_state SET wait_reason='context_unavailable' WHERE wait_reason='sandbox_capacity';
ALTER TABLE code_review_pr_state DROP CONSTRAINT code_review_pr_state_wait_reason_check;
ALTER TABLE code_review_pr_state ADD CONSTRAINT code_review_pr_state_wait_reason_check
    CHECK (wait_reason IN ('','quiet_period','minimum_interval','draft','manual_pause','policy_disabled','already_approved','active_review','context_unavailable'));
