package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewWorkspaceStore keeps workspace readiness and publication on the
// same database authority as job leases, review state, and sandbox holders.
type CodeReviewWorkspaceStore struct{ db TxStarter }

func NewCodeReviewWorkspaceStore(db TxStarter) *CodeReviewWorkspaceStore {
	return &CodeReviewWorkspaceStore{db: db}
}

// CodeReviewWorkspaceWaitErrorPrefix marks only workspace dependency waits as
// eligible for completion-driven scheduling; it grants no publication authority.
const CodeReviewWorkspaceWaitErrorPrefix = "waiting for code review workspace:"

type PublishCodeReviewWorkspaceParams struct {
	OrgID              uuid.UUID
	ReviewID           uuid.UUID
	SessionID          uuid.UUID
	PolicyID           uuid.UUID
	RepositoryID       uuid.UUID
	JobID              uuid.UUID
	ControllerJobID    uuid.UUID
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin review workspace publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCodeReviewWorkspace(ctx, tx, orgID, p.ReviewID, p.SessionID); err != nil {
		return false, err
	}
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
		  AND j.lease_expires_at > clock_timestamp()
		  AND m.session_id = @session_id AND m.policy_id = @policy_id
		  AND m.repository_id = @repository_id AND m.head_sha = @head_sha
		  AND m.status IN ('queued', 'running') AND m.stale = false
		  AND s.origin = 'code_review' AND s.status NOT IN ('completed', 'failed', 'cancelled')
		  AND s.revision_context->>'head_sha' = m.head_sha
		  AND s.container_id IS NULL AND s.snapshot_key IS NULL
		  AND NOT s.turn_holding_container
		  AND s.workspace_generation = @generation
		  AND n.status = 'active' AND n.last_heartbeat_at >= clock_timestamp() - interval '90 seconds'
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
			@owner_node_id, @lock_token, 'active', clock_timestamp(), clock_timestamp() + interval '60 seconds'
		)
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE SET container_id = EXCLUDED.container_id,
			owner_node_id = EXCLUDED.owner_node_id, lease_token = EXCLUDED.lease_token,
			status = 'active', heartbeat_at = clock_timestamp(), expires_at = EXCLUDED.expires_at,
			created_at = now(), released_at = NULL, updated_at = now()
		WHERE h.expires_at <= clock_timestamp()
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
	if err := nudgeCodeReviewWorkspaceController(ctx, tx, orgID, p); err != nil {
		return false, err
	}
	live, err := reviewWorkspacePreparationLeaseLive(ctx, tx, orgID, p)
	if err != nil || !live {
		return false, err
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
	if orgID == uuid.Nil || p.OrgID != orgID || p.ReviewID == uuid.Nil || p.SessionID == uuid.Nil || p.ContainerID == "" || p.OwnerNodeID == "" || p.JobID == uuid.Nil || p.JobLockToken == uuid.Nil || p.ExpectedHead == "" || p.ExpectedGeneration < 0 {
		return false, fmt.Errorf("rearm review workspace: missing ownership identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin review workspace rearm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCodeReviewWorkspace(ctx, tx, orgID, p.ReviewID, p.SessionID); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `
		WITH eligible AS MATERIALIZED (
			SELECT m.id AS review_id
			FROM jobs j
			JOIN code_review_session_metadata m ON m.org_id = j.org_id AND m.id = @review_id
			JOIN sessions s ON s.org_id = m.org_id AND s.id = m.session_id
			JOIN nodes n ON n.id = @owner_node_id
			WHERE j.org_id = @org_id AND j.id = @job_id
			  AND j.job_type = 'prepare_code_review_workspace' AND j.status = 'running'
			  AND j.lock_token = @lock_token AND j.locked_by_node_id = @owner_node_id
			  AND j.lease_expires_at > clock_timestamp()
			  AND m.session_id = @session_id AND m.policy_id = @policy_id
			  AND m.repository_id = @repository_id AND m.head_sha = @head_sha
			  AND m.status IN ('queued', 'running') AND m.stale = false
			  AND s.origin = 'code_review' AND s.status NOT IN ('completed', 'failed', 'cancelled')
			  AND s.revision_context->>'head_sha' = m.head_sha
			  AND s.container_id = @container_id AND s.worker_node_id = @owner_node_id
			  AND s.workspace_generation = @generation
			  AND NOT s.turn_holding_container
			  AND n.status = 'active' AND n.last_heartbeat_at >= clock_timestamp() - interval '90 seconds'
			FOR UPDATE OF j, m, s
		)
		INSERT INTO session_sandbox_holders AS h (
			org_id, session_id, container_id, holder_kind, holder_id,
			owner_node_id, lease_token, status, heartbeat_at, expires_at
		)
		SELECT @org_id, @session_id, @container_id, 'code_review', eligible.review_id,
			@owner_node_id, @lock_token, 'active', clock_timestamp(), clock_timestamp() + interval '60 seconds'
		FROM eligible
		ON CONFLICT (org_id, session_id, holder_kind, holder_id)
			WHERE status IN ('active', 'draining')
		DO UPDATE SET container_id = EXCLUDED.container_id, owner_node_id = EXCLUDED.owner_node_id,
			lease_token = EXCLUDED.lease_token, status = 'active', heartbeat_at = now(),
			expires_at = EXCLUDED.expires_at, created_at = now(), released_at = NULL, updated_at = now()
		WHERE h.expires_at <= clock_timestamp()
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
	if err := nudgeCodeReviewWorkspaceController(ctx, tx, orgID, p); err != nil {
		return false, err
	}
	live, err := reviewWorkspacePreparationLeaseLive(ctx, tx, orgID, p)
	if err != nil || !live {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return true, fmt.Errorf("commit review workspace rearm (outcome uncertain): %w", err)
	}
	return true, nil
}

// Readiness is a database barrier. The owner must still probe physical
// liveness before reuse; a heartbeat/holder cannot prove Docker is alive.
func (s *CodeReviewWorkspaceStore) Readiness(ctx context.Context, orgID, reviewID, sessionID uuid.UUID, head string) (CodeReviewWorkspaceReadiness, error) {
	return codeReviewWorkspaceReadiness(ctx, s.db, orgID, reviewID, sessionID, head, uuid.Nil)
}

func codeReviewWorkspaceReadiness(ctx context.Context, q DBTX, orgID, reviewID, sessionID uuid.UUID, head string, controllerJobID uuid.UUID) (CodeReviewWorkspaceReadiness, error) {
	var r CodeReviewWorkspaceReadiness
	query := `
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
		  AND h.status = 'active' AND h.expires_at > clock_timestamp()
		  AND n.status = 'active' AND n.last_heartbeat_at >= clock_timestamp() - interval '90 seconds'`
	args := pgx.NamedArgs{"org_id": orgID, "review_id": reviewID, "session_id": sessionID, "head_sha": head}
	if controllerJobID != uuid.Nil {
		// The controller row is already locked. Match its publication identity
		// in this fresh readiness snapshot rather than before any row wait.
		query += ` AND EXISTS (
            SELECT 1 FROM jobs j
            WHERE j.org_id = m.org_id AND j.id = @controller_job_id
              AND j.payload->>'policy_id' = m.policy_id::text
              AND j.payload->>'repository_id' = m.repository_id::text
              AND j.payload->>'review_output_key' = m.review_output_key
        )`
		args["controller_job_id"] = controllerJobID
	}
	query += ` LIMIT 1`
	err := q.QueryRow(ctx, query, args).Scan(&r.ContainerID, &r.OwnerNodeID, &r.Generation)
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

// lockCodeReviewWorkspace serializes readiness publication with controller retry.
// It must be the first statement in each transaction, before any row locks.
// This lock orders work only; every write still checks its own tenant and lease.
func lockCodeReviewWorkspace(ctx context.Context, tx pgx.Tx, orgID, reviewID, sessionID uuid.UUID) error {
	key := "code-review-workspace:" + orgID.String() + ":" + reviewID.String() + ":" + sessionID.String()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return fmt.Errorf("lock review workspace readiness transition: %w", err)
	}
	return nil
}

func nudgeCodeReviewWorkspaceController(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, p PublishCodeReviewWorkspaceParams) error {
	if p.ControllerJobID == uuid.Nil {
		return nil // Preparation payloads from before controller wakes remain valid.
	}
	_, err := tx.Exec(ctx, `
		UPDATE jobs j SET run_at = LEAST(j.run_at, clock_timestamp()), updated_at = clock_timestamp()
		FROM code_review_session_metadata m
		WHERE j.org_id = @org_id AND j.id = @controller_job_id
		  AND j.job_type = 'run_code_review' AND j.status = 'pending'
		  AND j.last_error LIKE @wait_prefix
		  AND m.org_id = j.org_id AND m.id = @review_id AND m.session_id = @session_id
		  AND m.policy_id = @policy_id AND m.repository_id = @repository_id AND m.head_sha = @head_sha
		  AND m.status IN ('queued','running') AND NOT m.stale
		  AND j.payload->>'org_id' = m.org_id::text
		  AND j.payload->>'metadata_id' = m.id::text AND j.payload->>'session_id' = m.session_id::text
		  AND j.payload->>'policy_id' = m.policy_id::text AND j.payload->>'repository_id' = m.repository_id::text
		  AND j.payload->>'head_sha' = m.head_sha AND j.payload->>'review_output_key' = m.review_output_key`, pgx.NamedArgs{
		"org_id": orgID, "controller_job_id": p.ControllerJobID, "review_id": p.ReviewID, "wait_prefix": CodeReviewWorkspaceWaitErrorPrefix + "%",
		"session_id": p.SessionID, "policy_id": p.PolicyID, "repository_id": p.RepositoryID, "head_sha": p.ExpectedHead,
	})
	if err != nil {
		return fmt.Errorf("wake prepared review workspace controller: %w", err)
	}
	return nil
}

// reviewWorkspacePreparationLeaseLive rechecks the already locked job after
// potentially waiting on row or holder conflicts, including node liveness.
// Transaction-start now() must not authorize ownership that expired during waits.
func reviewWorkspacePreparationLeaseLive(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, p PublishCodeReviewWorkspaceParams) (bool, error) {
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM jobs j JOIN nodes n ON n.id = @owner_node_id
		WHERE j.org_id = @org_id AND j.id = @job_id
		AND j.job_type = 'prepare_code_review_workspace' AND j.status = 'running'
		AND j.lock_token = @lock_token AND j.locked_by_node_id = @owner_node_id
		AND j.lease_expires_at > clock_timestamp()
		AND n.status = 'active' AND n.last_heartbeat_at >= clock_timestamp() - interval '90 seconds')`, pgx.NamedArgs{
		"org_id": orgID, "job_id": p.JobID, "lock_token": p.JobLockToken, "owner_node_id": p.OwnerNodeID,
	}).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("recheck review workspace publication lease: %w", err)
	}
	return live, nil
}

type RetryCodeReviewWorkspaceControllerParams struct {
	OrgID        uuid.UUID
	ReviewID     uuid.UUID
	SessionID    uuid.UUID
	JobID        uuid.UUID
	JobLockToken uuid.UUID
	ExpectedHead string
	RunAt        time.Time
	ErrorMessage string
}

// RetryControllerWithLease releases only the current controller's live lease.
// Readiness is reread after taking the publication lock, so either publication
// advances this pending retry or this retry sees the committed ready workspace.
func (s *CodeReviewWorkspaceStore) RetryControllerWithLease(ctx context.Context, orgID uuid.UUID, p RetryCodeReviewWorkspaceControllerParams) (time.Time, bool, error) {
	if orgID == uuid.Nil || p.OrgID != orgID || p.ReviewID == uuid.Nil || p.SessionID == uuid.Nil || p.JobID == uuid.Nil || p.JobLockToken == uuid.Nil || p.ExpectedHead == "" || p.RunAt.IsZero() {
		return time.Time{}, false, fmt.Errorf("retry review workspace controller: missing ownership identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("begin review workspace controller retry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockCodeReviewWorkspace(ctx, tx, orgID, p.ReviewID, p.SessionID); err != nil {
		return time.Time{}, false, err
	}
	args := pgx.NamedArgs{"org_id": orgID, "review_id": p.ReviewID, "session_id": p.SessionID, "job_id": p.JobID, "lock_token": p.JobLockToken, "head_sha": p.ExpectedHead}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT j.id
		FROM jobs j
		WHERE j.org_id = @org_id AND j.id = @job_id AND j.job_type = 'run_code_review'
		  AND j.status = 'running' AND j.lock_token = @lock_token AND j.lease_expires_at > clock_timestamp()
		  AND j.payload->>'org_id' = @org_id::uuid::text AND j.payload->>'metadata_id' = @review_id::uuid::text
		  AND j.payload->>'session_id' = @session_id::uuid::text AND j.payload->>'head_sha' = @head_sha
		FOR UPDATE OF j`, args).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("lock review workspace controller retry: %w", err)
	}
	ready, err := codeReviewWorkspaceReadiness(ctx, tx, orgID, p.ReviewID, p.SessionID, p.ExpectedHead, p.JobID)
	if err != nil {
		return time.Time{}, false, err
	}
	var runAt time.Time
	err = tx.QueryRow(ctx, `UPDATE jobs SET status='pending',last_error=@last_error,
		run_at=CASE WHEN @ready THEN LEAST(@run_at::timestamptz,clock_timestamp()) ELSE @run_at::timestamptz END,
		attempts=GREATEST(attempts-1,0),locked_by_node_id=NULL,run_owner_id=NULL,
		owner_kind='worker',lock_token=NULL,locked_at=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
		WHERE org_id=@org_id AND id=@job_id AND job_type='run_code_review'
		  AND status='running' AND lock_token=@lock_token AND lease_expires_at > clock_timestamp()
		RETURNING run_at`, pgx.NamedArgs{"org_id": orgID, "job_id": p.JobID, "lock_token": p.JobLockToken, "last_error": p.ErrorMessage, "ready": ready.Ready, "run_at": p.RunAt}).Scan(&runAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("retry review workspace controller: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return time.Time{}, false, fmt.Errorf("commit review workspace controller retry: %w", err)
	}
	return runAt, true, nil
}
