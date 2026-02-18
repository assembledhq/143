package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSchedulerLock_TryAcquire(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface)
		expectLocked bool
		expectErr    bool
	}{
		{
			name: "successfully acquires lock",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT pg_try_advisory_lock").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
			},
			expectLocked: true,
		},
		{
			name: "lock already held returns false",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT pg_try_advisory_lock").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(false))
			},
			expectLocked: false,
		},
		{
			name: "database error returns error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT pg_try_advisory_lock").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)
			sl := NewSchedulerLock(mock)

			acquired, err := sl.TryAcquire(context.Background())
			if tt.expectErr {
				require.Error(t, err, "TryAcquire should return an error")
			} else {
				require.NoError(t, err, "TryAcquire should not return an error")
				require.Equal(t, tt.expectLocked, acquired, "acquired should match expected value")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSchedulerLock_Release(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "successfully releases lock",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("SELECT pg_advisory_unlock").
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("SELECT", 1))
			},
		},
		{
			name: "database error returns error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("SELECT pg_advisory_unlock").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)
			sl := NewSchedulerLock(mock)

			err = sl.Release(context.Background())
			if tt.expectErr {
				require.Error(t, err, "Release should return an error")
			} else {
				require.NoError(t, err, "Release should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
