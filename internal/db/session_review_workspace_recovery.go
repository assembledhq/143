package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RecoverIncompleteCodeReviewWorkspaceParams struct {
	SessionID            uuid.UUID
	ContainerID          string
	WorkspaceGeneration  int64
	Repository           string
	BaseSHA              string
	HeadSHA              string
	PreparationJobID     uuid.UUID
	PreparationLockToken uuid.UUID
	WaitingJobID         uuid.UUID
	WaitingLockToken     uuid.UUID
}

var ErrReviewPreparationOwnershipUnavailable = errors.New("review preparation ownership record unavailable; restart the review workspace")

func (s *SessionStore) RecoverIncompleteCodeReviewWorkspace(ctx context.Context, orgID uuid.UUID, p RecoverIncompleteCodeReviewWorkspaceParams) (bool, error) {
	if orgID == uuid.Nil || p.SessionID == uuid.Nil || p.ContainerID == "" || p.WorkspaceGeneration < 0 || p.Repository == "" || p.BaseSHA == "" || p.HeadSHA == "" ||
		p.PreparationJobID == uuid.Nil || p.PreparationLockToken == uuid.Nil || p.WaitingJobID == uuid.Nil || p.WaitingLockToken == uuid.Nil {
		return false, fmt.Errorf("incomplete review workspace recovery requires exact preparation and waiting-job ownership")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var ownershipLost bool
	err := s.db.QueryRow(ctx, `SELECT COALESCE(
 owner.job_type IN ('run_agent','continue_session') AND owner.payload->>'session_id'=$3::text
 AND (owner.status<>'running' OR owner.lock_token IS DISTINCT FROM $4 OR owner.lease_expires_at<=now())
 AND NOT EXISTS(SELECT 1 FROM session_executors executor
   WHERE executor.org_id=owner.org_id AND executor.job_id=owner.id AND executor.lock_token=$4
   AND executor.status IN ('starting','running','draining')
   AND (executor.lease_expires_at IS NULL OR executor.lease_expires_at>now())),false)
 FROM jobs owner WHERE owner.org_id=$1 AND owner.id=$2`, orgID, p.PreparationJobID, p.SessionID, p.PreparationLockToken).Scan(&ownershipLost)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrReviewPreparationOwnershipUnavailable
	}
	if err != nil {
		return false, fmt.Errorf("inspect review preparation lease: %w", err)
	}
	if !ownershipLost {
		return false, nil
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin incomplete review workspace recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='250ms'; SET LOCAL statement_timeout='750ms'`); err != nil {
		return false, fmt.Errorf("bound review workspace recovery locks: %w", err)
	}
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT id FROM jobs WHERE org_id=$1 AND id=ANY($2)
 UNION SELECT id FROM jobs WHERE org_id=$1 AND queue='agent' AND status IN ('pending','running')
   AND job_type IN ('run_agent','continue_session','prepare_code_review_workspace') AND payload->>'session_id'=$3)
 SELECT job.id FROM jobs job JOIN candidates ON candidates.id=job.id
 WHERE job.org_id=$1 ORDER BY job.id FOR NO KEY UPDATE OF job`, orgID, []uuid.UUID{p.PreparationJobID, p.WaitingJobID}, p.SessionID.String())
	if err != nil {
		return false, fmt.Errorf("lock review workspace job ownership: %w", err)
	}
	lockedJobs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, fmt.Errorf("read review workspace job ownership: %w", err)
	}
	if !slices.Contains(lockedJobs, p.PreparationJobID) || !slices.Contains(lockedJobs, p.WaitingJobID) {
		return false, ErrReviewPreparationOwnershipUnavailable
	}
	var sessionID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM sessions
 WHERE org_id=$1 AND id=$2 AND origin='code_review'
 AND status NOT IN ('completed','failed','cancelled')
 AND container_id=$3 AND workspace_generation=$4
 AND revision_context->>'github_repo'=$5
 AND revision_context->>'base_sha'=$6 AND revision_context->>'head_sha'=$7
 FOR NO KEY UPDATE`, orgID, p.SessionID, p.ContainerID, p.WorkspaceGeneration, p.Repository, p.BaseSHA, p.HeadSHA).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock incomplete review workspace: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT id FROM session_executors
 WHERE org_id=$1 AND session_id=$2 AND status IN ('starting','running','draining')
 ORDER BY id FOR NO KEY UPDATE`, orgID, p.SessionID)
	if err != nil {
		return false, fmt.Errorf("lock review preparation executor: %w", err)
	}
	if _, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return false, fmt.Errorf("read review preparation executor: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT job.id FROM jobs job
 JOIN session_threads thread ON thread.org_id=job.org_id AND thread.session_id=$2
   AND job.payload->>'thread_id'=thread.id::text
 WHERE job.org_id=$1 AND job.id=ANY($4) AND job.payload->>'session_id'=$2::text AND job.id<>$3
 AND job.job_type='continue_session' AND job.status='running'
 AND job.lock_token IS NOT NULL AND job.lease_expires_at>now()
 AND thread.status='running' AND thread.current_turn=0
 AND thread.execution_mode='review' AND thread.filesystem_mode='read_only' AND thread.created_by_source='system'
 AND thread.cancel_requested_at IS NULL AND thread.archived_at IS NULL
 AND EXISTS(SELECT 1 FROM session_executors executor WHERE executor.org_id=job.org_id
   AND executor.session_id=thread.session_id AND executor.thread_id=thread.id
   AND executor.job_id=job.id AND executor.lock_token=job.lock_token
   AND executor.status IN ('starting','running') AND executor.lease_expires_at>now())
 AND EXISTS(SELECT 1 FROM code_review_agent_results result WHERE result.org_id=thread.org_id
   AND result.session_id=thread.session_id AND result.structured_result->>'thread_id'=thread.id::text
   AND result.role='reviewer' AND result.status IN ('queued','running')
   AND result.structured_result->>'read_only'='true')
 AND NOT EXISTS(SELECT 1 FROM code_review_agent_results result WHERE result.org_id=thread.org_id
   AND result.session_id=thread.session_id AND result.structured_result->>'thread_id'=thread.id::text
   AND result.role<>'reviewer')
 AND NOT EXISTS(SELECT 1 FROM thread_runtimes runtime WHERE runtime.org_id=thread.org_id
   AND runtime.session_id=thread.session_id AND runtime.thread_id=thread.id)
 ORDER BY thread.id FOR UPDATE OF thread`, orgID, p.SessionID, p.PreparationJobID, lockedJobs)
	if err != nil {
		return false, fmt.Errorf("prove review continuations waiting before runtime: %w", err)
	}
	waitingPeers, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return false, fmt.Errorf("read review continuation waiting proofs: %w", err)
	}
	if waitingPeers == nil {
		waitingPeers = []uuid.UUID{}
	}
	rows, err = tx.Query(ctx, `SELECT runtime.id FROM thread_runtimes runtime
 WHERE runtime.org_id=$1 AND runtime.session_id=$2
 AND EXISTS(SELECT 1 FROM session_executors executor
   WHERE executor.org_id=runtime.org_id AND executor.session_id=runtime.session_id
   AND executor.job_id=$3 AND executor.lock_token=$4 AND executor.thread_id=runtime.thread_id)
 ORDER BY runtime.id FOR NO KEY UPDATE OF runtime`, orgID, p.SessionID, p.PreparationJobID, p.PreparationLockToken)
	if err != nil {
		return false, fmt.Errorf("lock review preparation runtime: %w", err)
	}
	if _, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return false, fmt.Errorf("read review preparation runtime: %w", err)
	}
	rows, err = tx.Query(ctx, `SELECT id FROM session_sandbox_holders
 WHERE org_id=$1 AND session_id=$2 AND container_id=$3 AND status IN ('active','draining')
 ORDER BY id FOR NO KEY UPDATE`, orgID, p.SessionID, p.ContainerID)
	if err != nil {
		return false, fmt.Errorf("lock review workspace holders: %w", err)
	}
	if _, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
		return false, fmt.Errorf("read review workspace holders: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE sessions s
 SET container_id=NULL,worker_node_id=NULL,turn_holding_container=false,
 sandbox_state='none',snapshot_key=NULL,workspace_generation=workspace_generation+1
 WHERE s.org_id=$1 AND s.id=$2 AND s.container_id=$3 AND s.workspace_generation=$4
 AND EXISTS(SELECT 1 FROM jobs owner WHERE owner.org_id=s.org_id AND owner.id=$5
   AND owner.job_type IN ('run_agent','continue_session')
   AND owner.payload->>'session_id'=s.id::text
   AND (owner.status<>'running' OR owner.lock_token IS DISTINCT FROM $6 OR owner.lease_expires_at<=now()))
 AND EXISTS(SELECT 1 FROM jobs waiter WHERE waiter.org_id=s.org_id AND waiter.id=$7
   AND waiter.job_type='continue_session' AND waiter.payload->>'session_id'=s.id::text
   AND waiter.status='running' AND waiter.lock_token=$8 AND waiter.lease_expires_at>now())
 AND NOT EXISTS(SELECT 1 FROM jobs other WHERE other.org_id=s.org_id AND other.id<>$7
   AND NOT (other.id=ANY($9))
   AND other.queue='agent' AND other.job_type IN ('run_agent','continue_session','prepare_code_review_workspace')
   AND other.payload->>'session_id'=s.id::text AND other.status='running'
   AND (other.lease_expires_at IS NULL OR other.lease_expires_at>now()))
 AND NOT EXISTS(SELECT 1 FROM session_executors executor WHERE executor.org_id=s.org_id AND executor.session_id=s.id
   AND executor.status IN ('starting','running','draining')
   AND (executor.job_id<>$7 OR executor.lock_token<>$8)
   AND NOT EXISTS(SELECT 1 FROM jobs peer WHERE peer.org_id=executor.org_id AND peer.id=ANY($9)
     AND executor.job_id=peer.id AND executor.lock_token=peer.lock_token
     AND peer.payload->>'thread_id'=executor.thread_id::text)
   AND (executor.job_id<>$5 OR executor.lock_token<>$6
     OR executor.lease_expires_at IS NULL OR executor.lease_expires_at>now()))
 AND NOT EXISTS(SELECT 1 FROM thread_runtimes runtime WHERE runtime.org_id=s.org_id AND runtime.session_id=s.id
   AND runtime.status IN ('starting','live','paused','draining')
   AND (runtime.lease_expires_at IS NULL OR runtime.lease_expires_at>now()
     OR NOT EXISTS(SELECT 1 FROM session_executors owner_executor
       WHERE owner_executor.org_id=runtime.org_id AND owner_executor.session_id=runtime.session_id
       AND owner_executor.job_id=$5 AND owner_executor.lock_token=$6 AND owner_executor.thread_id=runtime.thread_id)))
 AND NOT EXISTS(SELECT 1 FROM session_sandbox_holders holder WHERE holder.org_id=s.org_id AND holder.session_id=s.id
   AND holder.holder_kind<>'code_review' AND holder.status IN ('active','draining') AND holder.expires_at>now())
 AND NOT EXISTS(SELECT 1 FROM preview_instances preview WHERE preview.org_id=s.org_id AND preview.session_id=s.id
   AND preview.preview_holding_container)
 AND NOT EXISTS(SELECT 1 FROM session_cancel_requests cancel WHERE cancel.org_id=s.org_id AND cancel.session_id=s.id
   AND cancel.delivered_at IS NULL)`, orgID, p.SessionID, p.ContainerID, p.WorkspaceGeneration, p.PreparationJobID, p.PreparationLockToken, p.WaitingJobID, p.WaitingLockToken, waitingPeers)
	if err != nil {
		return false, fmt.Errorf("recover incomplete review workspace: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET status='pending',locked_by_node_id=NULL,
 run_owner_id=NULL,owner_kind='worker',lock_token=NULL,locked_at=NULL,lease_expires_at=NULL,
 run_at=now(),updated_at=now(),last_error='review input preparation lease lost; queued for recovery'
 WHERE org_id=$1 AND id=$2 AND lock_token=$3 AND status='running' AND lease_expires_at<=now()`, orgID, p.PreparationJobID, p.PreparationLockToken); err != nil {
		return false, fmt.Errorf("fence expired review preparation job: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH lost_runtimes AS (
 UPDATE thread_runtimes runtime SET status='lost',lease_expires_at=NULL,
 closed_at=COALESCE(closed_at,now()),stop_reason=COALESCE(stop_reason,'review_preparation_lease_expired'),
 last_error=COALESCE(last_error,'review input preparation lease lost'),updated_at=now()
 WHERE runtime.org_id=$1 AND runtime.session_id=$2
 AND runtime.status IN ('starting','live','paused','draining') AND runtime.lease_expires_at<=now()
 AND EXISTS(SELECT 1 FROM session_executors executor WHERE executor.org_id=runtime.org_id
   AND executor.session_id=runtime.session_id AND executor.job_id=$3 AND executor.lock_token=$4
   AND executor.thread_id=runtime.thread_id)
 RETURNING runtime.id,runtime.org_id,runtime.session_id),
 expired_holders AS (
 UPDATE session_sandbox_holders holder SET status='expired',released_at=COALESCE(released_at,now()),updated_at=now()
 FROM lost_runtimes lost WHERE holder.org_id=lost.org_id AND holder.session_id=lost.session_id
 AND holder.holder_kind='thread_runtime' AND holder.holder_id=lost.id AND holder.status IN ('active','draining')),
 reset_inbox AS (
 UPDATE thread_inbox_entries inbox SET delivery_state='pending',runtime_id=NULL,owner_node_id=NULL,
 last_error=COALESCE(last_error,'runtime lease expired before ack'),updated_at=now()
 FROM lost_runtimes lost WHERE inbox.org_id=lost.org_id AND inbox.session_id=lost.session_id AND inbox.runtime_id=lost.id AND inbox.delivery_state='delivering')
 UPDATE thread_inbox_entries inbox SET delivery_state='unknown_delivery',
 last_error=COALESCE(last_error,'runtime lease expired after live delivery before ack'),updated_at=now()
 FROM lost_runtimes lost WHERE inbox.org_id=lost.org_id AND inbox.session_id=lost.session_id AND inbox.runtime_id=lost.id AND inbox.delivery_state='delivered'`, orgID, p.SessionID, p.PreparationJobID, p.PreparationLockToken); err != nil {
		return false, fmt.Errorf("fence expired review preparation runtime: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE session_executors
 SET status='lost',completed_at=now(),last_error='review input preparation lease lost',updated_at=now()
 WHERE org_id=$1 AND session_id=$2 AND job_id=$3 AND lock_token=$4
 AND status IN ('starting','running','draining') AND lease_expires_at<=now()`, orgID, p.SessionID, p.PreparationJobID, p.PreparationLockToken); err != nil {
		return false, fmt.Errorf("fence expired review preparation executor: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE session_sandbox_holders
 SET status='expired',released_at=COALESCE(released_at,now()),updated_at=now()
 WHERE org_id=$1 AND session_id=$2 AND container_id=$3
 AND (holder_kind='code_review' OR expires_at<=now())
 AND status IN ('active','draining')`, orgID, p.SessionID, p.ContainerID); err != nil {
		return false, fmt.Errorf("expire incomplete review workspace retention: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit incomplete review workspace recovery: %w", err)
	}
	return true, nil
}
