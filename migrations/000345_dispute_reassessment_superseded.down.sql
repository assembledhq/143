-- Older workers understand failed as the terminal state for a retired owner.
UPDATE code_review_decision_disputes
SET reassessment_status = 'failed', updated_at = now(), version = version + 1
WHERE reassessment_status = 'superseded';

ALTER TABLE code_review_decision_disputes
    DROP CONSTRAINT chk_code_review_decision_disputes_reassessment_status;

ALTER TABLE code_review_decision_disputes
    ADD CONSTRAINT code_review_decision_disputes_reassessment_status_check
        CHECK (reassessment_status IN (
            'not_requested', 'queued', 'running', 'completed', 'deduped', 'failed'
        ));
