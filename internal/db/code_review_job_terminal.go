package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// DeadLetterCodeReviewWithLease retires the controller and its eligible full
// assessment in one statement. Recovery sees either the active controller or
// the failed assessment, never a dead-lettered controller with resumable work.
// Uncertain and confirmed publications retain their existing recovery fences.
func (s *JobStore) DeadLetterCodeReviewWithLease(ctx context.Context, orgID, jobID, lockToken uuid.UUID, errMsg, failureDetail string) (bool, error) {
	var owned bool
	err := s.db.QueryRow(ctx, `WITH dead_lettered AS (
 UPDATE jobs SET status='dead_letter',last_error=$4,completed_at=now(),
 locked_by_node_id=NULL,run_owner_id=NULL,owner_kind='worker',lock_token=NULL,
 locked_at=NULL,lease_expires_at=NULL,updated_at=now()
 WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3
 AND job_type='run_code_review'
 RETURNING org_id,payload
), failed_assessment AS (
 UPDATE code_review_revision_assessments a SET status='failed',failure_detail=$5,completed_at=now()
 FROM dead_lettered j
 WHERE a.org_id=$1 AND j.org_id=a.org_id AND a.review_scope='full'
 AND a.session_id::text=j.payload->>'session_id'
 AND a.publication_key=j.payload->>'review_output_key'
 AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 AND ((a.status IN ('reserved','running') AND a.publication_state='not_started')
   OR (a.status='publishing' AND a.publication_state='reserved'))
 RETURNING a.id
)
SELECT EXISTS(SELECT 1 FROM dead_lettered)`, orgID, jobID, lockToken, errMsg, failureDetail).Scan(&owned)
	if err != nil {
		return false, fmt.Errorf("dead-letter full review and assessment with lease: %w", err)
	}
	return owned, nil
}
