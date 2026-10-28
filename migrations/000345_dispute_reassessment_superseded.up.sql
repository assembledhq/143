ALTER TABLE code_review_decision_disputes
    DROP CONSTRAINT code_review_decision_disputes_reassessment_status_check;

ALTER TABLE code_review_decision_disputes
    ADD CONSTRAINT chk_code_review_decision_disputes_reassessment_status
        CHECK (reassessment_status IN (
            'not_requested', 'queued', 'running', 'completed', 'deduped', 'superseded', 'failed'
        ));

-- Repair only the known scheduling projection that lost its explanation.
-- Other stale owners may represent a genuine replacement-start failure, so
-- retain their explicit details and outcomes. Never attach an unrelated winner
-- or change provider reply receipts while repairing historical presentation.
UPDATE code_review_decision_disputes dispute
SET reassessment_status = 'superseded',
    reassessment_decision = NULL,
    reassessment_flipped = NULL,
    status_detail = 'The reassessment was superseded because the review target changed. The objection remains recorded for a policy owner.',
    updated_at = now(), version = dispute.version + 1
FROM code_review_session_metadata owner
WHERE dispute.reassessment_status = 'failed'
  AND owner.org_id = dispute.org_id
  AND owner.session_id = dispute.reassessment_session_id
  AND owner.pull_request_id = dispute.pull_request_id
  AND owner.repository_id = dispute.repository_id
  AND owner.status = 'stale'
  AND owner.failure_reason = 'review scheduling target superseded'
  AND NULLIF(btrim(dispute.status_detail), '') IS NULL;
