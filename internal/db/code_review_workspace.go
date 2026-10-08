package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewWorkspaceStore keeps workspace readiness and publication on the
// same database authority as job leases, review state, and sandbox holders.
type CodeReviewWorkspaceStore struct{ db TxStarter }

func NewCodeReviewWorkspaceStore(db TxStarter) *CodeReviewWorkspaceStore {
	return &CodeReviewWorkspaceStore{db: db}
}

type PublishCodeReviewWorkspaceParams struct {
	OrgID              uuid.UUID
	ReviewID           uuid.UUID
	SessionID          uuid.UUID
	PolicyID           uuid.UUID
	RepositoryID       uuid.UUID
	JobID              uuid.UUID
	JobLockToken       uuid.UUID
	OwnerNodeID        string
	ContainerID        string
	ExpectedHead       string
	ExpectedGeneration int64
}

// PublishPrepared locks the live job, review, and session before publishing.
// A worker that lost its lease, a cancelled review, or a concurrent container
// publisher cannot attach an orphaned holder or overwrite the winning sandbox.
func (s *CodeReviewWorkspaceStore) PublishPrepared(ctx context.Context, orgID uuid.UUID, p PublishCodeReviewWorkspaceParams) (bool, error) {
	if orgID == uuid.Nil || p.OrgID != orgID || p.ReviewID == uuid.Nil || p.SessionID == uuid.Nil || p.JobID == uuid.Nil || p.JobLockToken == uuid.Nil || p.OwnerNodeID == "" || p.ContainerID == "" || p.ExpectedHead == "" || p.ExpectedGeneration < 0 {
		return false, fmt.Errorf("publish review workspace: missing ownership identity")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin review workspace publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var generation int64
	err = tx.QueryRow(ctx, `
		SELECT s.workspace_generation
		FROM jobs j
		JOIN code_review_session_metadata m ON m.org_id = j.org_id AND m.id = @review_id
		JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
		JOIN nodes n ON n.id = @owner_node_id
		WHERE j.org_id = @org_id AND j.id = @job_id
		  AND j.job_type = 'prepare_code_review_workspace' AND j.status = 'running'
		  AND j.lock_token = @lock_token AND j.locked_by_node_id = @owner_node_id
		  AND j.lease_expires_at > now()
		  AND m.session_id = @session_id AND m.policy_id = @policy_id
		  AND m.repository_id = @repository_id AND m.head_sha = @head_sha
		  AND m.status IN ('queued', 'running') AND m.stale = false
		  AND s.origin = 'code_review' AND s.status NOT IN ('completed', 'failed', 'cancelled')
		  AND s.revision_context->>'head_sha' = m.head_sha
		  AND s.container_id IS NULL AND s.snapshot_key IS NULL
		  AND NOT s.turn_holding_container
		  AND s.workspace_generation = @generation
		  AND n.status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'
		FOR UPDATE OF j, m, s`, pgx.NamedArgs{
		"org_id": orgID, "review_id": p.ReviewID, "session_id": p.SessionID,
		"policy_id": p.PolicyID, "repository_id": p.RepositoryID,
		"job_id": p.JobID, "lock_token": p.JobLockToken,
		"owner_node_id": p.OwnerNodeID, "head_sha": p.ExpectedHead,
		"generation": p.ExpectedGeneration,
	}).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock review workspace publication: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE sessions SET container_id = @container_id, worker_node_id = @owner_node_id,
			sandbox_state = 'running', workspace_generation = workspace_generation + 1
		WHERE org_id = @org_id AND id = @session_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": p.SessionID, "container_id": p.ContainerID,
		"owner_node_id": p.OwnerNodeID,
	}); err != nil {
		return false, fmt.Errorf("publish review workspace container: %w", err)
	}
	var holderID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO session_sandbox_holders AS h (
			org_id, session_id, container_id, holder_kind, holder_id,
			owner_node_id, lease_token, status, heartbeat_at, expires_at
		) VALUES (
			@org_id, @session_id, @container_id, 'code_review', @review_id,
			@owner_node_id, @lock_token, 'active', now(), now() + interval '60 seconds'
		)
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE SET container_id = EXCLUDED.container_id,
			owner_node_id = EXCLUDED.owner_node_id, lease_token = EXCLUDED.lease_token,
			status = 'active', heartbeat_at = now(), expires_at = EXCLUDED.expires_at,
			created_at = now(), released_at = NULL, updated_at = now()
		WHERE h.expires_at <= now()
		RETURNING id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": p.SessionID, "container_id": p.ContainerID,
		"review_id": p.ReviewID, "owner_node_id": p.OwnerNodeID, "lock_token": p.JobLockToken,
	}).Scan(&holderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // rollback the session update; a live holder still owns it
	}
	if err != nil {
		return false, fmt.Errorf("publish review workspace holder: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		// A broken connection can report an error after PostgreSQL committed.
		// The caller must retain this container until reconciliation proves it
		// is unreferenced; destroying it could break a published review.
		return true, fmt.Errorf("commit review workspace publication (outcome uncertain): %w", err)
	}
	return true, nil
}

type CodeReviewWorkspaceReadiness struct {
	ContainerID string
	OwnerNodeID string
	Generation  int64
	Ready       bool
}

// RearmExisting is for an owner that has just verified its recorded Docker
// container is alive, but its bounded idle holder expired. It never changes
// the session's container or generation. The same lease and review-state
// fence prevents a stale initializer from reviving a cancelled review.
func (s *CodeReviewWorkspaceStore) RearmExisting(ctx context.Context, orgID uuid.UUID, p PublishCodeReviewWorkspaceParams) (bool, error) {
	if orgID == uuid.Nil || p.OrgID != orgID || p.ContainerID == "" || p.OwnerNodeID == "" || p.JobID == uuid.Nil || p.JobLockToken == uuid.Nil {
		return false, fmt.Errorf("rearm review workspace: missing ownership identity")
	}
	rows, err := s.db.Query(ctx, `
		WITH eligible AS MATERIALIZED (
			SELECT m.id AS review_id
			FROM jobs j
			JOIN code_review_session_metadata m ON m.org_id = j.org_id AND m.id = @review_id
			JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
			JOIN nodes n ON n.id = @owner_node_id
			WHERE j.org_id = @org_id AND j.id = @job_id
			  AND j.job_type = 'prepare_code_review_workspace' AND j.status = 'running'
			  AND j.lock_token = @lock_token AND j.locked_by_node_id = @owner_node_id
			  AND j.lease_expires_at > now()
			  AND m.session_id = @session_id AND m.policy_id = @policy_id
			  AND m.repository_id = @repository_id AND m.head_sha = @head_sha
			  AND m.status IN ('queued', 'running') AND m.stale = false
			  AND s.origin = 'code_review' AND s.status NOT IN ('completed', 'failed', 'cancelled')
			  AND s.revision_context->>'head_sha' = m.head_sha
			  AND s.container_id = @container_id AND s.worker_node_id = @owner_node_id
			  AND s.workspace_generation = @generation
			  AND NOT s.turn_holding_container
			  AND n.status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'
			FOR UPDATE OF j, m, s
		)
		INSERT INTO session_sandbox_holders AS h (
			org_id, session_id, container_id, holder_kind, holder_id,
			owner_node_id, lease_token, status, heartbeat_at, expires_at
		)
		SELECT @org_id, @session_id, @container_id, 'code_review', eligible.review_id,
			@owner_node_id, @lock_token, 'active', now(), now() + interval '60 seconds'
		FROM eligible
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE SET container_id = EXCLUDED.container_id, owner_node_id = EXCLUDED.owner_node_id,
			lease_token = EXCLUDED.lease_token, status = 'active', heartbeat_at = now(),
			expires_at = EXCLUDED.expires_at, created_at = now(), released_at = NULL, updated_at = now()
		WHERE h.expires_at <= now()
		RETURNING h.id`, pgx.NamedArgs{
		"org_id": orgID, "review_id": p.ReviewID, "session_id": p.SessionID,
		"policy_id": p.PolicyID, "repository_id": p.RepositoryID,
		"job_id": p.JobID, "lock_token": p.JobLockToken,
		"owner_node_id": p.OwnerNodeID, "head_sha": p.ExpectedHead,
		"generation": p.ExpectedGeneration, "container_id": p.ContainerID,
	})
	if err != nil {
		return false, fmt.Errorf("rearm live review workspace holder: %w", err)
	}
	_, err = pgx.CollectOneRow(rows, pgx.RowTo[uuid.UUID])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("rearm live review workspace holder: %w", err)
	}
	return true, nil
}

// Readiness is a database barrier. The owner must still probe physical
// liveness before reuse; a heartbeat/holder cannot prove Docker is alive.
func (s *CodeReviewWorkspaceStore) Readiness(ctx context.Context, orgID, reviewID, sessionID uuid.UUID, head string) (CodeReviewWorkspaceReadiness, error) {
	var r CodeReviewWorkspaceReadiness
	err := s.db.QueryRow(ctx, `
		SELECT s.container_id, s.worker_node_id, s.workspace_generation
		FROM code_review_session_metadata m
		JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
		JOIN session_sandbox_holders h ON h.org_id = m.org_id AND h.session_id = m.session_id
			AND h.holder_kind = 'code_review' AND h.holder_id = m.id
		JOIN nodes n ON n.id = s.worker_node_id
		WHERE m.org_id = @org_id AND m.id = @review_id AND m.session_id = @session_id
		  AND m.head_sha = @head_sha AND m.status IN ('queued', 'running') AND m.stale = false
		  AND s.origin = 'code_review' AND s.status NOT IN ('completed', 'failed', 'cancelled')
		  AND s.revision_context->>'head_sha' = m.head_sha
		  AND h.container_id = s.container_id AND h.owner_node_id = s.worker_node_id
		  AND h.status = 'active' AND h.expires_at > now()
		  AND n.status = 'active' AND n.last_heartbeat_at >= now() - interval '90 seconds'
		LIMIT 1`, pgx.NamedArgs{
		"org_id": orgID, "review_id": reviewID, "session_id": sessionID, "head_sha": head,
	}).Scan(&r.ContainerID, &r.OwnerNodeID, &r.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, fmt.Errorf("check code review workspace readiness: %w", err)
	}
	r.Ready = true
	return r, nil
}

// ReconcileMissing clears a missing container only on its owner and only after
// the owner has probed Docker (or that node is marked dead). It expires the
// old review holder; the next successful publication advances the generation.
func (s *CodeReviewWorkspaceStore) ReconcileMissing(ctx context.Context, orgID, reviewID, sessionID uuid.UUID, containerID, ownerNodeID, currentNodeID string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin missing review workspace recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT s.id FROM sessions s
		JOIN code_review_session_metadata m ON m.org_id = s.org_id AND m.session_id = s.id
		WHERE s.org_id = @org_id AND s.id = @session_id AND m.id = @review_id
		  AND m.status IN ('queued', 'running') AND m.stale = false
		  AND s.container_id = @container_id AND s.worker_node_id = @owner_node_id
		  AND (@current_node_id = @owner_node_id OR EXISTS (
			SELECT 1 FROM nodes n WHERE n.id = @owner_node_id AND n.status = 'dead'))
		  AND NOT s.turn_holding_container
		  AND NOT EXISTS (SELECT 1 FROM session_sandbox_holders other
			WHERE other.org_id = s.org_id AND other.session_id = s.id
			  AND other.holder_kind <> 'code_review' AND other.status IN ('active', 'draining')
			  AND other.expires_at > now())
		  AND NOT EXISTS (SELECT 1 FROM preview_instances p
			WHERE p.org_id = s.org_id AND p.session_id = s.id AND p.preview_holding_container)
		FOR UPDATE OF s, m`, pgx.NamedArgs{
		"org_id": orgID, "review_id": reviewID, "session_id": sessionID,
		"container_id": containerID, "owner_node_id": ownerNodeID,
		"current_node_id": currentNodeID,
	}).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock missing review workspace: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_sandbox_holders SET status = 'expired',
			released_at = COALESCE(released_at, now()), updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id
		  AND holder_kind = 'code_review' AND holder_id = @review_id
		  AND container_id = @container_id AND owner_node_id = @owner_node_id
		  AND status IN ('active', 'draining')`, pgx.NamedArgs{
		"org_id": orgID, "review_id": reviewID, "session_id": sessionID,
		"container_id": containerID, "owner_node_id": ownerNodeID,
	}); err != nil {
		return false, fmt.Errorf("expire missing review workspace holder: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET container_id = NULL, worker_node_id = NULL,
			sandbox_state = CASE WHEN snapshot_key IS NULL THEN 'none' ELSE 'snapshotted' END
		WHERE org_id = @org_id AND id = @session_id
		  AND container_id = @container_id AND worker_node_id = @owner_node_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
		"container_id": containerID, "owner_node_id": ownerNodeID,
	}); err != nil {
		return false, fmt.Errorf("clear missing review workspace: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit missing review workspace recovery: %w", err)
	}
	return true, nil
}
