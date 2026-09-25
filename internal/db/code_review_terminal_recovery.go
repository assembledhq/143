package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReconcileTerminalReviews repairs admission blockers left by a superseded or
// failed full review. The PR lock serializes recovery with replacement admission.
// Publication receipts and evidence-only turns have their own recovery paths.
func (s *CodeReviewScheduleStore) ReconcileTerminalReviews(ctx context.Context, orgID, repoID, prID uuid.UUID) error {
	return s.WithLockedPR(ctx, orgID, repoID, prID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		return reconcileTerminalReviews(ctx, tx, orgID, prID, state)
	})
}

func reconcileTerminalReviews(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID, state *models.CodeReviewPRState) error {
	rows, err := tx.Query(ctx, `UPDATE code_review_revision_assessments a
 SET status=CASE m.status WHEN 'stale' THEN 'superseded' WHEN 'cancelled' THEN 'cancelled' ELSE 'failed' END,
     completed_at=COALESCE(a.completed_at,now()),
     superseded_at=CASE WHEN m.status='stale' THEN COALESCE(a.superseded_at,now()) ELSE a.superseded_at END,
     failure_detail=COALESCE(a.failure_detail,'full review ended with status '||m.status)
 FROM code_review_session_metadata m
 WHERE a.org_id=$1 AND a.pull_request_id=$2 AND a.review_scope='full'
   AND m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
   AND m.pull_request_id=a.pull_request_id AND m.status IN ('stale','failed','cancelled')
   AND ((a.status IN ('reserved','running') AND a.publication_state='not_started')
     OR (a.status='publishing' AND a.publication_state='reserved'))
   AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
 RETURNING a.id`, orgID, prID)
	if err != nil {
		return fmt.Errorf("reconcile terminal full assessments: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE code_review_requests SET status='failed' WHERE org_id=$1 AND pull_request_id=$2 AND assessment_id=ANY($3::uuid[]) AND status IN ('pending','joined')`, orgID, prID, ids); err != nil {
			return err
		}
		for _, id := range ids {
			if state.ActiveAssessmentID != nil && *state.ActiveAssessmentID == id {
				state.ActiveAssessmentID = nil
				state.ActiveSessionID = nil
				if state.PendingInput == nil && state.State == models.CodeReviewScheduleRunning {
					state.State = models.CodeReviewScheduleIdle
					state.WaitReason = models.CodeReviewWaitNone
					state.RetryAt = nil
					state.EligibleAt = nil
				}
			}
		}
	}
	// A failed controller may never have requested cancellation. Recover only threads
	// with a terminal executor AND terminal executor job, and no remaining runtime
	// or queued execution. Active owners remain blockers even after lease expiry.
	// A lost executor alone is insufficient: its bounded recovery job must also end.
	_, err = tx.Exec(ctx, `UPDATE session_threads t
 SET status=CASE WHEN t.cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE 'failed' END,
     completed_at=COALESCE(t.completed_at,now()),last_activity_at=now()
 FROM code_review_session_metadata m
 WHERE t.org_id=$1 AND m.org_id=t.org_id AND m.session_id=t.session_id AND m.pull_request_id=$2
   AND m.status IN ('stale','failed','cancelled')
   AND t.status IN ('pending','running','awaiting_input')
   AND (t.cancel_requested_at IS NOT NULL OR m.status='failed')
   AND EXISTS(SELECT 1 FROM session_executors e JOIN jobs j ON j.org_id=e.org_id AND j.id=e.job_id
     WHERE e.org_id=t.org_id AND e.thread_id=t.id AND e.session_id=t.session_id
       AND e.status IN ('failed','completed','lost') AND j.status IN ('failed','dead_letter','succeeded','cancelled'))
   AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=t.org_id AND e.session_id=t.session_id AND e.status IN ('starting','running','draining'))
   AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=t.org_id AND r.session_id=t.session_id AND r.status IN ('starting','live','paused','draining'))
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=t.org_id AND j.status IN ('pending','running')
     AND j.job_type IN ('run_agent','continue_session','fork_session_thread','revert_session_thread','deliver_thread_inbox')
     AND j.payload->>'session_id'=t.session_id::text)
   AND NOT EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=t.org_id AND d.session_id=t.session_id AND d.status IN ('pending','running'))`, orgID, prID)
	if err != nil {
		return fmt.Errorf("reconcile terminal review threads: %w", err)
	}
	return nil
}
