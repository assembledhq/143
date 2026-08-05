UPDATE code_review_decision_disputes
SET routing = 'not_a_dispute',
    intake_status = 'discarded',
    adjudication_status = NULL,
    updated_at = now(),
    version = version + 1
WHERE routing = 'review_request';

ALTER TABLE code_review_decision_disputes
    DROP CONSTRAINT code_review_decision_disputes_routing_check;

ALTER TABLE code_review_decision_disputes
    ADD CONSTRAINT code_review_decision_disputes_routing_check
        CHECK (routing IS NULL OR routing IN (
            'reassess',
            'policy_signal_only',
            'answer_only',
            'not_a_dispute'
        ));
