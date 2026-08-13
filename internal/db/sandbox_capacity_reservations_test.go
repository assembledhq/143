package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSandboxCapacityReservationStore_ReserveSandboxCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		reserved         int
		effectiveMax     int
		conflicting      bool
		expectInsert     bool
		expectedTotal    int
		expectedAcquired bool
		expectConflict   bool
	}{
		{name: "reserves below shared capacity", reserved: 1, effectiveMax: 3, expectInsert: true, expectedTotal: 3, expectedAcquired: true},
		{name: "rejects at shared capacity", reserved: 2, effectiveMax: 3, expectedTotal: 3},
		{name: "reports prior job attempt lease as coordination", reserved: 1, effectiveMax: 3, conflicting: true, expectedTotal: 2, expectConflict: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			jobID, lockToken, reservationID := uuid.New(), uuid.New(), uuid.New()
			expiresAt := time.Now().Add(time.Minute)
			conflictingExpiresAt := time.Now().Add(30 * time.Second)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs("worker-1").
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*node_id = @node_id`).
				WithArgs("worker-1").
				WillReturnResult(pgxmock.NewResult("DELETE", 0))
			mock.ExpectQuery(`(?s)SELECT id.*FROM jobs.*status = 'running'.*lock_token = @job_lock_token.*FOR UPDATE`).
				WithArgs(&jobID, &lockToken).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*job_id = @job_id.*expires_at <= now\(\).*job_lock_token IS DISTINCT FROM @job_lock_token`).
				WithArgs(&jobID, &lockToken).
				WillReturnResult(pgxmock.NewResult("DELETE", 0))
			mock.ExpectQuery(`(?s)WITH active_capacity_keys AS.*FROM jobs reserved_job.*UNION.*FROM sandbox_capacity_reservations.*SELECT COUNT`).
				WithArgs("worker-1", &jobID, &lockToken).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.reserved))
			conflictRows := pgxmock.NewRows([]string{"expires_at"})
			if tt.conflicting {
				conflictRows.AddRow(conflictingExpiresAt)
			}
			mock.ExpectQuery(`(?s)SELECT expires_at.*FROM sandbox_capacity_reservations.*job_id = @job_id.*job_lock_token IS DISTINCT FROM @job_lock_token.*ORDER BY expires_at DESC.*LIMIT 1`).
				WithArgs(&jobID, &lockToken).
				WillReturnRows(conflictRows)
			if tt.expectInsert {
				mock.ExpectQuery(`(?s)INSERT INTO sandbox_capacity_reservations.*job_lock_token.*ON CONFLICT \(job_id\).*job_lock_token = EXCLUDED.job_lock_token.*WHERE sandbox_capacity_reservations.job_lock_token IS NOT DISTINCT FROM EXCLUDED.job_lock_token.*RETURNING id`).
					WithArgs("worker-1", &jobID, &lockToken, models.SandboxWorkloadClassInteractive, expiresAt).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(reservationID))
			}
			mock.ExpectCommit()
			mock.ExpectRollback()

			store := NewSandboxCapacityReservationStore(mock)
			actualID, live, total, acquired, err := store.ReserveSandboxCapacity(
				context.Background(), "worker-1", &jobID, &lockToken, models.SandboxWorkloadClassInteractive,
				func(context.Context) (int, error) { return 1, nil }, tt.effectiveMax, expiresAt,
			)

			if tt.expectConflict {
				require.ErrorIs(t, err, ErrSandboxCapacityAttemptConflict, "replacement attempt should report stale-attempt coordination separately from fleet saturation")
				var conflictErr *SandboxCapacityAttemptConflictError
				require.ErrorAs(t, err, &conflictErr, "replacement attempt should expose the stale lease deadline")
				require.Equal(t, conflictingExpiresAt, conflictErr.ExpiresAt, "replacement attempt should wait for the persisted stale lease expiry")
			} else {
				require.NoError(t, err, "shared capacity reservation should complete")
			}
			require.Equal(t, 1, live, "shared capacity reservation should report the live Docker count")
			require.Equal(t, tt.expectedTotal, total, "shared capacity reservation should report the combined live and reserved load")
			require.Equal(t, tt.expectedAcquired, acquired, "shared capacity reservation should enforce the effective worker limit")
			if tt.expectInsert {
				require.Equal(t, reservationID, actualID, "successful admission should return the persisted reservation id")
			} else {
				require.Equal(t, uuid.Nil, actualID, "rejected admission should not return a reservation id")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "shared admission should lock, count, and reserve in one transaction")
		})
	}
}

func TestSandboxCapacityReservationStore_ReserveSandboxCapacityFailsClosed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("worker-1").
		WillReturnError(errors.New("lock unavailable"))
	mock.ExpectRollback()

	reservationID, _, _, acquired, err := NewSandboxCapacityReservationStore(mock).ReserveSandboxCapacity(
		context.Background(), "worker-1", nil, nil, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 2, time.Now().Add(time.Minute),
	)

	require.ErrorContains(t, err, "lock unavailable", "shared capacity admission should surface coordination failures")
	require.False(t, acquired, "failed shared capacity admission should fail closed")
	require.Equal(t, uuid.Nil, reservationID, "failed shared capacity admission should not return a reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "failed admission should roll back its transaction")
}

func TestSandboxCapacityReservationStore_ReserveSandboxCapacityRejectsStaleJobAttempt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, staleLockToken := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("worker-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*node_id = @node_id`).
		WithArgs("worker-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectQuery(`(?s)SELECT id.*FROM jobs.*status = 'running'.*lock_token = @job_lock_token.*FOR UPDATE`).
		WithArgs(&jobID, &staleLockToken).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	reservationID, _, _, acquired, err := NewSandboxCapacityReservationStore(mock).ReserveSandboxCapacity(
		context.Background(), "worker-1", &jobID, &staleLockToken, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 2, time.Now().Add(time.Minute),
	)

	require.ErrorContains(t, err, "job attempt no longer owns", "stale queue attempts must fail final admission")
	require.False(t, acquired, "stale queue attempts must not acquire shared capacity")
	require.Equal(t, uuid.Nil, reservationID, "stale queue attempts must not receive a reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "stale attempt admission should stop before counting or inserting capacity")
}

func TestSandboxCapacityReservationStore_CountsLiveSandboxesInsideTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	countErr := errors.New("docker unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("worker-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*node_id = @node_id`).
		WithArgs("worker-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))
	mock.ExpectRollback()

	reservationID, live, total, acquired, err := NewSandboxCapacityReservationStore(mock).ReserveSandboxCapacity(
		context.Background(),
		"worker-1",
		nil,
		nil,
		models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, countErr },
		2,
		time.Now().Add(time.Minute),
	)

	require.ErrorIs(t, err, countErr, "shared admission should propagate the authoritative Docker count failure")
	require.Equal(t, uuid.Nil, reservationID, "failed live counting should not insert a shared reservation")
	require.Equal(t, 0, live, "failed live counting should not report a partial Docker count")
	require.Equal(t, 0, total, "failed live counting should not report a misleading combined load")
	require.False(t, acquired, "failed live counting should fail admission closed")
	require.NoError(t, mock.ExpectationsWereMet(), "live counting should occur only after the transaction and node lock are acquired")
}

func TestSandboxCapacityReservationStore_ReleaseSandboxCapacity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	reservationID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WithArgs("worker-1").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	lockToken := uuid.New()
	mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*WHERE id = @reservation_id.*AND node_id = @node_id.*job_lock_token = @job_lock_token`).
		WithArgs(reservationID, "worker-1", &lockToken).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	err = NewSandboxCapacityReservationStore(mock).ReleaseSandboxCapacity(context.Background(), "worker-1", reservationID, &lockToken)

	require.NoError(t, err, "shared capacity release should delete the reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "shared capacity release should hold the worker admission lock and target the exact reservation")
}
