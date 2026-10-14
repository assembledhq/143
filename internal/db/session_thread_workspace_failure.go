package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FailWorkspaceWaitForDeadLetter records failure of the exact unstarted full-
// review thread whose workspace-readiness job exhausted its retry window. The
// review controller retains parent/assessment ownership and harvests this
// thread result through its normal failure and fallback paths.
func (s *SessionThreadStore) FailWorkspaceWaitForDeadLetter(ctx context.Context, orgID, sessionID, threadID, jobID uuid.UUID, expectedTurn int, detail string) (bool, error) {
	if orgID == uuid.Nil || sessionID == uuid.Nil || threadID == uuid.Nil || jobID == uuid.Nil || expectedTurn < 1 || detail == "" {
		return false, fmt.Errorf("invalid workspace-wait terminal identity")
	}
	starter, ok := s.db.(TxStarter)
	if !ok {
		return false, fmt.Errorf("workspace-wait terminal reconciliation requires transaction support")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pullRequestID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT pull_request_id FROM code_review_revision_assessments
 WHERE org_id=$1 AND session_id=$2 AND review_scope='full' ORDER BY generation DESC LIMIT 1`, orgID, sessionID).Scan(&pullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Match the scheduler's PR lock before reading assessment ownership. Lock
	// the thread with FOR UPDATE as well so runtime creation cannot slip past
	// the no-runtime proof through its thread foreign key.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+orgID.String()+":"+pullRequestID.String()); err != nil {
		return false, err
	}
	var lockedThread uuid.UUID
	err = tx.QueryRow(ctx, `SELECT t.id FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id
 WHERE s.org_id=$1 AND s.id=$2 AND t.id=$3 AND t.current_turn=$4-1
 AND s.origin='code_review' AND s.status='running' AND t.status='running'
 AND t.cancel_requested_at IS NULL AND t.archived_at IS NULL FOR UPDATE OF s,t`, orgID, sessionID, threadID, expectedTurn).Scan(&lockedThread)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE session_threads t SET status='failed',completed_at=now(),
 failure_explanation=$6,failure_category='sandbox_workspace_not_ready'
 WHERE t.org_id=$1 AND t.session_id=$2 AND t.id=$3 AND t.current_turn=$4-1 AND t.status='running'
 AND t.cancel_requested_at IS NULL AND t.archived_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM session_cancel_requests c WHERE c.org_id=t.org_id AND c.session_id=t.session_id AND c.delivered_at IS NULL)
 AND EXISTS(SELECT 1 FROM jobs j WHERE j.org_id=t.org_id AND j.id=$5 AND j.status='dead_letter'
   AND j.job_type='continue_session' AND j.lock_token IS NULL AND j.lease_expires_at IS NULL
   AND j.payload->>'session_id'=t.session_id::text AND j.payload->>'thread_id'=t.id::text)
 AND EXISTS(SELECT 1 FROM code_review_revision_assessments a
   JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
   JOIN code_review_pr_state p ON p.org_id=a.org_id AND p.pull_request_id=a.pull_request_id AND p.repository_id=a.repository_id
   WHERE a.org_id=t.org_id AND a.session_id=t.session_id AND a.review_scope='full' AND a.status='running'
     AND a.publication_state='not_started' AND a.publication_receipt IS NULL AND a.github_review_id IS NULL
     AND a.result_origin IS NULL AND m.status='running' AND m.github_review_id IS NULL
     AND p.active_session_id=a.session_id AND (p.active_assessment_id IS NULL OR p.active_assessment_id=a.id))
 AND NOT EXISTS(SELECT 1 FROM jobs replacement WHERE replacement.org_id=t.org_id AND replacement.id<>$5
   AND replacement.status IN ('pending','running') AND replacement.job_type='continue_session'
   AND replacement.payload->>'session_id'=t.session_id::text AND replacement.payload->>'thread_id'=t.id::text)
 AND NOT EXISTS(SELECT 1 FROM session_executors e WHERE e.org_id=t.org_id AND e.session_id=t.session_id
   AND e.thread_id=t.id AND e.job_id<>$5 AND e.status IN ('starting','running','draining'))
 AND NOT EXISTS(SELECT 1 FROM thread_runtimes r WHERE r.org_id=t.org_id AND r.session_id=t.session_id
   AND r.thread_id=t.id AND r.status IN ('starting','live','paused','draining'))`, orgID, sessionID, threadID, expectedTurn, jobID, detail)
	if err != nil {
		return false, fmt.Errorf("fail dead-lettered workspace wait: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		s.publishThreadRuntimeByID(ctx, orgID, threadID)
	}
	return tag.RowsAffected() > 0, nil
}
