package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNodeManager_Register(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "successful registration",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080", []byte("{}")).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name: "upsert on conflict updates existing node",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080", []byte("{}")).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "database error returns error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080", []byte("{}")).
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
			nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")

			err = nm.Register(context.Background(), "localhost:8080")
			if tt.expectErr {
				require.Error(t, err, "Register should return an error")
			} else {
				require.NoError(t, err, "Register should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestNodeManager_Heartbeat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "successful heartbeat update",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE nodes").
					WithArgs("node-1", "active", []byte("{}")).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "heartbeat database error",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("UPDATE nodes").
					WithArgs("node-1", "active", []byte("{}")).
					WillReturnError(fmt.Errorf("connection lost"))
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
			nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")

			err = nm.HeartbeatOnce(context.Background())
			if tt.expectErr {
				require.Error(t, err, "HeartbeatOnce should return an error")
			} else {
				require.NoError(t, err, "HeartbeatOnce should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestNodeManager_RequestDrain(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE nodes SET status = 'draining'").
		WithArgs("node-1", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	err = nm.RequestDrain(context.Background(), time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err, "RequestDrain should not return an error")
	require.True(t, nm.IsDraining(), "RequestDrain should flip the local draining flag")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeManager_MarkStaleNodesDead(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	staleBefore := time.Now().Add(-2 * time.Minute)
	mock.ExpectExec("UPDATE nodes SET status = 'dead'").
		WithArgs(staleBefore).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	updated, err := nm.MarkStaleNodesDead(context.Background(), staleBefore)
	require.NoError(t, err, "MarkStaleNodesDead should not return an error")
	require.Equal(t, int64(2), updated, "MarkStaleNodesDead should return the number of stale nodes marked dead")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
