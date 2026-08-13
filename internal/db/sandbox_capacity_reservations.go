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

// SandboxCapacityReservationStore coordinates final worker-local admission
// across the main worker and isolated session-executor processes.
type SandboxCapacityReservationStore struct {
	db TxStarter
}

func NewSandboxCapacityReservationStore(db TxStarter) *SandboxCapacityReservationStore {
	return &SandboxCapacityReservationStore{db: db}
}

// ReserveSandboxCapacity atomically reserves one worker slot after combining
// routed job reservations with short-lived final-admission leases. jobID may be
// nil for non-job consumers such as previews. The current job is excluded from
// the routed count because this call replaces its durable routing reservation
// with a final-admission lease.
// lint:allow-no-orgid reason="worker-local capacity admission intentionally coordinates jobs and previews across organizations"
func (s *SandboxCapacityReservationStore) ReserveSandboxCapacity(
	ctx context.Context,
	nodeID string,
	jobID, jobLockToken *uuid.UUID,
	workloadClass models.SandboxWorkloadClass,
	countLiveSandboxes func(context.Context) (int, error),
	effectiveMax int,
	expiresAt time.Time,
) (uuid.UUID, int, int, bool, error) {
	if s == nil || s.db == nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("reserve sandbox capacity: store is not configured")
	}
	if countLiveSandboxes == nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("reserve sandbox capacity: live sandbox counter is not configured")
	}
	if (jobID == nil) != (jobLockToken == nil) {
		return uuid.Nil, 0, 0, false, fmt.Errorf("reserve sandbox capacity: job id and lock token must be provided together")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("begin sandbox capacity reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Use the same per-node advisory namespace as durable job routing. This
	// closes the count/insert race between a routed turn and a preview or
	// executor doing final admission on the same Docker host.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(@node_id, 143))`, pgx.NamedArgs{
		"node_id": nodeID,
	}); err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("lock sandbox capacity for worker %s: %w", nodeID, err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM sandbox_capacity_reservations
		WHERE node_id = @node_id
		  AND expires_at <= now()`, pgx.NamedArgs{"node_id": nodeID}); err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("delete expired sandbox capacity reservations: %w", err)
	}
	if jobID != nil {
		var ownedJobID uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id
			FROM jobs
			WHERE id = @job_id
			  AND status = 'running'
			  AND lock_token = @job_lock_token
			FOR UPDATE`, pgx.NamedArgs{
			"job_id":         jobID,
			"job_lock_token": jobLockToken,
		}).Scan(&ownedJobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, 0, 0, false, fmt.Errorf("reserve sandbox capacity: job attempt no longer owns the running job")
		}
		if err != nil {
			return uuid.Nil, 0, 0, false, fmt.Errorf("validate sandbox capacity job attempt: %w", err)
		}
		// A reclaimed attempt may be routed to a different capacity node. Remove
		// an expired lease from the prior attempt before inserting so the unique
		// job index does not reuse its reservation identity. A late release from
		// the stale process must never be able to target the replacement lease.
		if _, err := tx.Exec(ctx, `
			DELETE FROM sandbox_capacity_reservations
			WHERE job_id = @job_id
			  AND expires_at <= now()
			  AND job_lock_token IS DISTINCT FROM @job_lock_token`, pgx.NamedArgs{
			"job_id":         jobID,
			"job_lock_token": jobLockToken,
		}); err != nil {
			return uuid.Nil, 0, 0, false, fmt.Errorf("delete expired prior sandbox capacity job attempt: %w", err)
		}
	}

	// Count only after taking the node lock. Otherwise another process could
	// finish creating a container and release its lease between this count and
	// the reservation insert, leaving admission based on a stale Docker view.
	liveSandboxes, err := countLiveSandboxes(ctx)
	if err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("count live sandboxes for shared admission: %w", err)
	}

	var reserved int
	err = tx.QueryRow(ctx, `
		WITH active_capacity_keys AS (
			SELECT 'job:' || reserved_job.id::text || ':attempt:' || COALESCE(reserved_job.lock_token::text, 'pending') AS capacity_key
			FROM jobs reserved_job
			JOIN nodes reserved_node ON reserved_node.id = reserved_job.target_node_id
			WHERE COALESCE(
					NULLIF(reserved_node.metadata->>'sandbox_capacity_node_id', ''),
					regexp_replace(reserved_node.id, '-g[0-9]{14}-[A-Za-z0-9._-]+$', '')
				) = @node_id
			  AND reserved_job.sandbox_slot_reserved_until > now()
			  AND reserved_job.status IN ('pending', 'running')
			  AND (
				@job_id::uuid IS NULL
				OR reserved_job.id <> @job_id
				OR reserved_job.lock_token IS DISTINCT FROM @job_lock_token
			  )
			UNION
			SELECT CASE
					WHEN local_reservation.job_id IS NOT NULL THEN 'job:' || local_reservation.job_id::text || ':attempt:' || COALESCE(local_reservation.job_lock_token::text, 'legacy')
					ELSE 'local:' || local_reservation.id::text
				END AS capacity_key
			FROM sandbox_capacity_reservations local_reservation
			WHERE local_reservation.node_id = @node_id
			  AND local_reservation.expires_at > now()
			  AND (
				@job_id::uuid IS NULL
				OR local_reservation.job_id IS DISTINCT FROM @job_id
				OR local_reservation.job_lock_token IS DISTINCT FROM @job_lock_token
			  )
		)
		SELECT COUNT(*) FROM active_capacity_keys`, pgx.NamedArgs{
		"job_id":         jobID,
		"job_lock_token": jobLockToken,
		"node_id":        nodeID,
	}).Scan(&reserved)
	if err != nil {
		return uuid.Nil, liveSandboxes, liveSandboxes, false, fmt.Errorf("count shared sandbox capacity reservations: %w", err)
	}
	total := liveSandboxes + reserved
	var conflictingJobAttempt bool
	if jobID != nil {
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM sandbox_capacity_reservations
				WHERE job_id = @job_id
				  AND expires_at > now()
				  AND job_lock_token IS DISTINCT FROM @job_lock_token
			)`, pgx.NamedArgs{
			"job_id":         jobID,
			"job_lock_token": jobLockToken,
		}).Scan(&conflictingJobAttempt)
		if err != nil {
			return uuid.Nil, liveSandboxes, total, false, fmt.Errorf("inspect prior sandbox capacity job attempt: %w", err)
		}
	}
	if conflictingJobAttempt || effectiveMax <= 0 || total >= effectiveMax {
		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, liveSandboxes, total, false, fmt.Errorf("commit rejected sandbox capacity reservation: %w", err)
		}
		return uuid.Nil, liveSandboxes, total, false, nil
	}

	var reservationID uuid.UUID
	// A job has one live admission lease. The attempt token makes retries by the
	// current owner idempotent; the conflicting-attempt check above prevents a
	// replacement owner from taking over the stale process's row before that
	// process releases it or its lease expires.
	err = tx.QueryRow(ctx, `
		INSERT INTO sandbox_capacity_reservations (
			node_id, job_id, job_lock_token, workload_class, expires_at
		)
		VALUES (@node_id, @job_id, @job_lock_token, @workload_class, @expires_at)
		ON CONFLICT (job_id) WHERE job_id IS NOT NULL
		DO UPDATE SET
			node_id = EXCLUDED.node_id,
			job_lock_token = EXCLUDED.job_lock_token,
			workload_class = EXCLUDED.workload_class,
			expires_at = EXCLUDED.expires_at
		WHERE sandbox_capacity_reservations.job_lock_token IS NOT DISTINCT FROM EXCLUDED.job_lock_token
		RETURNING id`, pgx.NamedArgs{
		"expires_at":     expiresAt,
		"job_id":         jobID,
		"job_lock_token": jobLockToken,
		"node_id":        nodeID,
		"workload_class": workloadClass,
	}).Scan(&reservationID)
	if err != nil {
		return uuid.Nil, liveSandboxes, total, false, fmt.Errorf("insert shared sandbox capacity reservation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, liveSandboxes, total, false, fmt.Errorf("commit shared sandbox capacity reservation: %w", err)
	}
	return reservationID, liveSandboxes, total + 1, true, nil
}

// ReleaseSandboxCapacity removes a final-admission lease once sandbox creation
// either completes or aborts. Release takes the same per-node advisory lock as
// admission so a new admission cannot observe the container before it appears
// in Docker while simultaneously missing the lease that protected its start.
// lint:allow-no-orgid reason="reservation id is globally unique and release runs before tenant context may be available"
func (s *SandboxCapacityReservationStore) ReleaseSandboxCapacity(ctx context.Context, nodeID string, reservationID uuid.UUID, jobLockToken *uuid.UUID) error {
	if s == nil || s.db == nil || reservationID == uuid.Nil {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin shared sandbox capacity release: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(@node_id, 143))`, pgx.NamedArgs{
		"node_id": nodeID,
	}); err != nil {
		return fmt.Errorf("lock sandbox capacity release for worker %s: %w", nodeID, err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM sandbox_capacity_reservations
		WHERE id = @reservation_id
		  AND node_id = @node_id
		  AND (
			(@job_lock_token::uuid IS NULL AND job_id IS NULL)
			OR job_lock_token = @job_lock_token
		  )`, pgx.NamedArgs{
		"job_lock_token": jobLockToken,
		"node_id":        nodeID,
		"reservation_id": reservationID,
	}); err != nil {
		return fmt.Errorf("release shared sandbox capacity reservation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit shared sandbox capacity release: %w", err)
	}
	return nil
}
