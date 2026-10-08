package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// StopFullReviewFallbackLoop serializes the restart budget with PR admission.
// Only provably unsent full reviews are eligible; publication recovery remains
// responsible for uncertain or confirmed sends.
func (s *CodeReviewScheduleStore) StopFullReviewFallbackLoop(ctx context.Context, orgID, assessmentID uuid.UUID, limit int, reason string) (bool, error) {
	if limit < 2 {
		return false, fmt.Errorf("review restart limit must be at least two")
	}
	a, err := s.GetAssessmentByID(ctx, orgID, assessmentID)
	if err != nil {
		return false, err
	}
	stopped := false
	err = s.WithLockedPR(ctx, orgID, a.RepositoryID, a.PullRequestID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		var stopErr error
		stopped, stopErr = stopFullReviewFallbackLoop(ctx, tx, a, limit, reason, state)
		return stopErr
	})
	return stopped, err
}

func stopFullReviewFallbackLoop(ctx context.Context, tx pgx.Tx, a models.CodeReviewAssessment, limit int, reason string, state *models.CodeReviewPRState) (bool, error) {
	var status, detail string
	if err := tx.QueryRow(ctx, `SELECT status,COALESCE(failure_detail,'') FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2 AND pull_request_id=$3 FOR UPDATE`, a.OrgID, a.ID, a.PullRequestID).Scan(&status, &detail); err != nil {
		return false, err
	}
	if status == "cancelled" && detail == string(models.CodeReviewStatusCodeLoopDetected) {
		return true, nil
	}
	// Following explicit previous-assessment links avoids counting unrelated
	// requests. A human request, changed analysis contract, successful result,
	// or different tenant/PR breaks the chain. The recursion is bounded.
	rows, err := tx.Query(ctx, `WITH RECURSIVE attempts AS (
 SELECT a.id,a.previous_assessment_id,a.session_id,a.head_sha,a.base_sha,a.base_ref,a.code_digest,a.contract_digest,a.intent_digest,
   s.revision_context->'request_context'->>'source' AS source,1 AS depth,ARRAY[a.id] AS path
 FROM code_review_revision_assessments a JOIN sessions s ON s.org_id=a.org_id AND s.id=a.session_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.pull_request_id=$3 AND a.review_scope='full'
 AND a.status IN ('failed','superseded') AND a.failure_detail LIKE 'full_review:%'
 AND a.publication_state IN ('not_started','reserved') AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 UNION ALL
 SELECT a.id,a.previous_assessment_id,a.session_id,a.head_sha,a.base_sha,a.base_ref,a.code_digest,a.contract_digest,a.intent_digest,
   s.revision_context->'request_context'->>'source',c.depth+1,c.path||a.id
 FROM attempts c JOIN code_review_revision_assessments a ON a.id=c.previous_assessment_id AND a.org_id=$1 AND a.pull_request_id=$3
 JOIN sessions s ON s.org_id=a.org_id AND s.id=a.session_id
 WHERE c.depth<$4 AND c.source='assessment_fallback' AND NOT a.id=ANY(c.path)
 AND a.review_scope='full' AND a.status IN ('failed','superseded')
 AND (a.failure_detail LIKE 'full_review_queued:%' OR a.failure_detail LIKE 'full_review:%')
 AND a.head_sha=c.head_sha AND a.base_sha=c.base_sha AND a.base_ref=c.base_ref
 AND a.code_digest=c.code_digest AND a.contract_digest=c.contract_digest AND a.intent_digest=c.intent_digest
 AND a.publication_state IN ('not_started','reserved') AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 ) SELECT session_id FROM attempts ORDER BY depth`, a.OrgID, a.ID, a.PullRequestID, limit)
	if err != nil {
		return false, err
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, err
	}
	if len(sessions) < limit {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE code_review_revision_assessments SET status='cancelled',failure_detail=$3,completed_at=COALESCE(completed_at,now()) WHERE org_id=$1 AND id=$2`, a.OrgID, a.ID, models.CodeReviewStatusCodeLoopDetected); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE code_review_session_metadata SET status='cancelled',stale=false,phase=NULL,status_code=$3,status_message=$4,failure_reason=$4,retry_at=NULL,retryable_failure=false,completed_at=COALESCE(completed_at,now()) WHERE org_id=$1 AND session_id=$2 AND pull_request_id=$5 AND status IN ('queued','running','stale','failed')`, a.OrgID, a.SessionID, models.CodeReviewStatusCodeLoopDetected, reason, a.PullRequestID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE session_threads SET cancel_requested_at=COALESCE(cancel_requested_at,now()) WHERE org_id=$1 AND session_id=ANY($2) AND status IN ('pending','running','awaiting_input')`, a.OrgID, sessions); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE code_review_requests SET status='cancelled' WHERE org_id=$1 AND pull_request_id=$2 AND (assessment_id=$3 OR session_id=$4) AND status IN ('pending','joined')`, a.OrgID, a.PullRequestID, a.ID, a.SessionID); err != nil {
		return false, err
	}
	// Persist the terminal comment with the cancellation so a crash cannot
	// leave GitHub advertising a review that will never restart.
	key := "code_review_status_comment:" + a.SessionID.String() + ":loop_stopped"
	if _, err = enqueueOn(ctx, tx, a.OrgID, EnqueueOpts{
		Queue: "default", JobType: models.JobTypeSyncCodeReviewStatusComment,
		Payload:  map[string]uuid.UUID{"org_id": a.OrgID, "repository_id": a.RepositoryID, "pull_request_id": a.PullRequestID, "session_id": a.SessionID},
		Priority: 3, DedupeKey: &key, MaxAttempts: 3,
	}); err != nil {
		return false, fmt.Errorf("enqueue stopped review status comment: %w", err)
	}
	if state.ActiveAssessmentID != nil && *state.ActiveAssessmentID == a.ID {
		state.ActiveAssessmentID = nil
	}
	if state.ActiveSessionID != nil && *state.ActiveSessionID == a.SessionID {
		state.ActiveSessionID = nil
	}
	// A newer explicit request or revision retains its pending work.
	if state.PendingInput == nil && state.ActiveSessionID == nil && state.ActiveAssessmentID == nil {
		state.State = models.CodeReviewScheduleIdle
		state.WaitReason = models.CodeReviewWaitNone
		state.RetryAt = nil
		state.EligibleAt = nil
	}
	return true, nil
}
