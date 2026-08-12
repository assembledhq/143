package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
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
		expectInsert     bool
		expectedTotal    int
		expectedAcquired bool
	}{
		{name: "reserves below shared capacity", reserved: 1, effectiveMax: 3, expectInsert: true, expectedTotal: 3, expectedAcquired: true},
		{name: "rejects at shared capacity", reserved: 2, effectiveMax: 3, expectedTotal: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			jobID, reservationID := uuid.New(), uuid.New()
			expiresAt := time.Now().Add(time.Minute)
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
				WithArgs("worker-1").
				WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*node_id = @node_id`).
				WithArgs("worker-1").
				WillReturnResult(pgxmock.NewResult("DELETE", 0))
			mock.ExpectQuery(`(?s)WITH active_capacity_keys AS.*FROM jobs reserved_job.*UNION.*FROM sandbox_capacity_reservations.*SELECT COUNT`).
				WithArgs("worker-1", &jobID).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.reserved))
			if tt.expectInsert {
				mock.ExpectQuery(`(?s)INSERT INTO sandbox_capacity_reservations.*ON CONFLICT \(job_id\).*RETURNING id`).
					WithArgs("worker-1", &jobID, models.SandboxWorkloadClassInteractive, expiresAt).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(reservationID))
			}
			mock.ExpectCommit()
			mock.ExpectRollback()

			store := NewSandboxCapacityReservationStore(mock)
			actualID, live, total, acquired, err := store.ReserveSandboxCapacity(
				context.Background(), "worker-1", &jobID, models.SandboxWorkloadClassInteractive,
				func(context.Context) (int, error) { return 1, nil }, tt.effectiveMax, expiresAt,
			)

			require.NoError(t, err, "shared capacity reservation should complete")
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
		context.Background(), "worker-1", nil, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 2, time.Now().Add(time.Minute),
	)

	require.ErrorContains(t, err, "lock unavailable", "shared capacity admission should surface coordination failures")
	require.False(t, acquired, "failed shared capacity admission should fail closed")
	require.Equal(t, uuid.Nil, reservationID, "failed shared capacity admission should not return a reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "failed admission should roll back its transaction")
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
	mock.ExpectExec(`(?s)DELETE FROM sandbox_capacity_reservations.*WHERE id = \$1`).
		WithArgs(reservationID).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = NewSandboxCapacityReservationStore(mock).ReleaseSandboxCapacity(context.Background(), reservationID)

	require.NoError(t, err, "shared capacity release should delete the reservation")
	require.NoError(t, mock.ExpectationsWereMet(), "shared capacity release should target the exact reservation")
}
