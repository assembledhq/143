SET LOCAL lock_timeout = '5s';

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
    status_detail = 'The reassessment was superseded because the review target changed. The objection remains recorded.',
    updated_at = now(), version = dispute.version + 1
FROM code_review_session_metadata owner
WHERE dispute.reassessment_status = 'failed'
  AND owner.org_id = dispute.org_id
  AND owner.session_id = dispute.reassessment_session_id
  AND owner.pull_request_id = dispute.pull_request_id
  AND owner.repository_id = dispute.repository_id
  AND owner.status = 'stale'
  AND owner.failure_reason = 'review scheduling target superseded'
  AND NULLIF(btrim(dispute.status_detail), '') IS NULL
  -- Any exact capture handoff history can represent a replacement-start
  -- failure. Leave those records for their owning recovery path to settle.
  AND NOT EXISTS (
      SELECT 1 FROM code_review_requests request
      WHERE request.org_id = dispute.org_id
        AND request.pull_request_id = dispute.pull_request_id
        AND request.source_kind = 'github'
        AND request.source_identity = 'capture_identity:' || owner.session_id::text
  )
  AND NOT EXISTS (
      SELECT 1 FROM jobs replacement
      WHERE replacement.org_id = dispute.org_id
        AND replacement.job_type = 'start_code_review_reassessment'
        AND replacement.dedupe_key = 'code_review_reassessment:' || owner.pull_request_id::text || ':capture_identity:' || owner.session_id::text
        AND replacement.payload->>'triggering_dispute_id' = dispute.id::text
  );
