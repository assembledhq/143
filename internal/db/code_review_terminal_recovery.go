package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Recovery must leave worker capacity available even when a PR lock or query is
// blocked. Failed recovery is retried; it must never bypass the admission fences.
const codeReviewTerminalRecoveryTimeout = 5 * time.Second

// ErrCodeReviewTerminalRecoveryTimeout marks a provider-free recovery wait.
// Queue consumers may defer it without spending the review's attempt budget.
// Caller cancellation is deliberately not marked as a recovery wait.
var ErrCodeReviewTerminalRecoveryTimeout = errors.New("code review terminal recovery timed out")

// ReconcileTerminalReviews repairs admission blockers left by a superseded or
// failed full review. The PR lock serializes recovery with replacement admission.
// Publication receipts and evidence-only turns have their own recovery paths.
func (s *CodeReviewScheduleStore) ReconcileTerminalReviews(ctx context.Context, orgID, repoID, prID uuid.UUID) (retErr error) {
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, codeReviewTerminalRecoveryTimeout)
	defer func() {
		retErr = terminalReviewRecoveryError(callerCtx, ctx, retErr)
		cancel()
	}()
	return s.WithLockedPR(ctx, orgID, repoID, prID, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
		return reconcileTerminalReviews(ctx, tx, orgID, prID, state)
	})
}

func reconcileTerminalReviews(ctx context.Context, tx pgx.Tx, orgID, prID uuid.UUID, state *models.CodeReviewPRState) (retErr error) {
	// Admission also invokes this helper inside its existing transaction. Bound
	// that path as well and propagate cancellation so its writes roll back.
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, codeReviewTerminalRecoveryTimeout)
	defer func() {
		retErr = terminalReviewRecoveryError(callerCtx, ctx, retErr)
		cancel()
	}()
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
	_, err = tx.Exec(ctx, reconcileTerminalReviewThreadsSQL, orgID, prID)
	if err != nil {
		return fmt.Errorf("reconcile terminal review threads: %w", err)
	}
	return nil
}

func terminalReviewRecoveryError(callerCtx, recoveryCtx context.Context, err error) error {
	if err != nil && recoveryCtx.Err() == context.DeadlineExceeded && callerCtx.Err() == nil {
		return fmt.Errorf("%w: %w", ErrCodeReviewTerminalRecoveryTimeout, err)
	}
	return err
}

// OFFSET 0 keeps terminal executor proof correlated to this exact session/thread.
// Without this barrier PostgreSQL can flatten EXISTS into an org-wide executor
// and job scan before applying the candidate PR/thread join.
const reconcileTerminalReviewThreadsSQL = `UPDATE session_threads t
 SET status=CASE WHEN t.cancel_requested_at IS NOT NULL THEN 'cancelled' ELSE 'failed' END,
     failure_category=CASE WHEN t.cancel_requested_at IS NULL THEN COALESCE(t.failure_category,'stuck_thread') ELSE t.failure_category END,
     failure_explanation=CASE WHEN t.cancel_requested_at IS NULL THEN COALESCE(t.failure_explanation,'Review controller failed after its executor and job ended; reconciled orphaned thread.') ELSE t.failure_explanation END,
     completed_at=COALESCE(t.completed_at,now()),last_activity_at=now()
 FROM code_review_session_metadata m
 WHERE t.org_id=$1 AND m.org_id=t.org_id AND m.session_id=t.session_id AND m.pull_request_id=$2
   AND m.status IN ('stale','failed','cancelled')
   AND t.status IN ('pending','running','awaiting_input')
   AND (t.cancel_requested_at IS NOT NULL OR m.status='failed')
   AND EXISTS(SELECT 1 FROM session_executors e JOIN jobs j ON j.org_id=e.org_id AND j.id=e.job_id
     WHERE e.org_id=t.org_id AND e.thread_id=t.id AND e.session_id=t.session_id
       AND e.status IN ('failed','completed','lost') AND j.status IN ('failed','dead_letter','succeeded','cancelled')
     OFFSET 0)
   AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=t.org_id AND e.session_id=t.session_id AND e.status IN ('starting','running','draining'))
   AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=t.org_id AND r.session_id=t.session_id AND r.status IN ('starting','live','paused','draining'))
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=t.org_id AND j.status IN ('pending','running')
     AND j.job_type IN ('run_agent','continue_session','fork_session_thread','revert_session_thread','deliver_thread_inbox')
     AND j.payload->>'session_id'=t.session_id::text)
   AND NOT EXISTS(SELECT 1 FROM code_review_recheck_dispatches d WHERE d.org_id=t.org_id AND d.session_id=t.session_id AND d.status IN ('pending','running'))`
