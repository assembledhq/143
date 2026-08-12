package db

import (
	"context"
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
	jobID *uuid.UUID,
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
			SELECT 'job:' || reserved_job.id::text AS capacity_key
			FROM jobs reserved_job
			WHERE reserved_job.target_node_id = @node_id
			  AND reserved_job.sandbox_slot_reserved_until > now()
			  AND reserved_job.status IN ('pending', 'running')
			  AND (@job_id::uuid IS NULL OR reserved_job.id <> @job_id)
			UNION
			SELECT CASE
					WHEN local_reservation.job_id IS NOT NULL THEN 'job:' || local_reservation.job_id::text
					ELSE 'local:' || local_reservation.id::text
				END AS capacity_key
			FROM sandbox_capacity_reservations local_reservation
			WHERE local_reservation.node_id = @node_id
			  AND local_reservation.expires_at > now()
			  AND (@job_id::uuid IS NULL OR local_reservation.job_id IS DISTINCT FROM @job_id)
		)
		SELECT COUNT(*) FROM active_capacity_keys`, pgx.NamedArgs{
		"job_id":  jobID,
		"node_id": nodeID,
	}).Scan(&reserved)
	if err != nil {
		return uuid.Nil, liveSandboxes, liveSandboxes, false, fmt.Errorf("count shared sandbox capacity reservations: %w", err)
	}
	total := liveSandboxes + reserved
	if effectiveMax <= 0 || total >= effectiveMax {
		if err := tx.Commit(ctx); err != nil {
			return uuid.Nil, liveSandboxes, total, false, fmt.Errorf("commit rejected sandbox capacity reservation: %w", err)
		}
		return uuid.Nil, liveSandboxes, total, false, nil
	}

	var reservationID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO sandbox_capacity_reservations (
			node_id, job_id, workload_class, expires_at
		)
		VALUES (@node_id, @job_id, @workload_class, @expires_at)
		ON CONFLICT (job_id) WHERE job_id IS NOT NULL
		DO UPDATE SET
			node_id = EXCLUDED.node_id,
			workload_class = EXCLUDED.workload_class,
			expires_at = EXCLUDED.expires_at
		RETURNING id`, pgx.NamedArgs{
		"expires_at":     expiresAt,
		"job_id":         jobID,
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
// either completes or aborts.
// lint:allow-no-orgid reason="reservation id is globally unique and release runs before tenant context may be available"
func (s *SandboxCapacityReservationStore) ReleaseSandboxCapacity(ctx context.Context, reservationID uuid.UUID) error {
	if s == nil || s.db == nil || reservationID == uuid.Nil {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
		DELETE FROM sandbox_capacity_reservations
		WHERE id = $1`, reservationID); err != nil {
		return fmt.Errorf("release shared sandbox capacity reservation: %w", err)
	}
	return nil
}
