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

func TestNodeManager_Register_UsesMetadataProviderAndSurfacesMarshalErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(nm *NodeManager, mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "includes metadata from provider",
			setup: func(nm *NodeManager, mock pgxmock.PgxPoolIface) {
				nm.SetMetadataProvider(func() map[string]any {
					return map[string]any{"build_sha": "abc123"}
				})
				mock.ExpectExec("INSERT INTO nodes").
					WithArgs("node-1", "worker", "localhost:8080", []byte(`{"build_sha":"abc123"}`)).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name: "returns metadata marshal errors before writing",
			setup: func(nm *NodeManager, mock pgxmock.PgxPoolIface) {
				nm.SetMetadataProvider(func() map[string]any {
					return map[string]any{"bad": make(chan int)}
				})
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

			nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
			tt.setup(nm, mock)

			err = nm.Register(context.Background(), "localhost:8080")
			if tt.expectErr {
				require.Error(t, err, "Register should return an error when metadata cannot be marshaled")
			} else {
				require.NoError(t, err, "Register should succeed with valid metadata")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
			}
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

func TestNodeManager_Heartbeat_UsesDrainStatusAndMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	nm.SetMetadataProvider(func() map[string]any {
		return map[string]any{"active_job_count": 2}
	})
	nm.draining = true

	mock.ExpectExec("UPDATE nodes").
		WithArgs("node-1", "draining", []byte(`{"active_job_count":2}`)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = nm.HeartbeatOnce(context.Background())
	require.NoError(t, err, "HeartbeatOnce should use draining status and provider metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeManager_StartHeartbeat_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	nm.heartbeatInterval = 5 * time.Millisecond

	mock.ExpectExec("UPDATE nodes").
		WithArgs("node-1", "active", []byte("{}")).
		WillReturnError(fmt.Errorf("connection lost"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		nm.StartHeartbeat(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("StartHeartbeat should stop when the context is cancelled")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeManager_RequestDrain(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE nodes").
		WithArgs("node-1", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	err = nm.RequestDrain(context.Background(), time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err, "RequestDrain should not return an error")
	require.True(t, nm.IsDraining(), "RequestDrain should flip the local draining flag")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeManager_RequestDrain_ReturnsDatabaseErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE nodes").
		WithArgs("node-1", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("write failed"))

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	err = nm.RequestDrain(context.Background(), time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC))
	require.Error(t, err, "RequestDrain should return database errors")
	require.True(t, nm.IsDraining(), "RequestDrain should leave the local drain bit set even when the write fails")
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

func TestNodeManager_MarkStaleNodesDead_ReturnsWrappedErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	staleBefore := time.Now().Add(-2 * time.Minute)
	mock.ExpectExec("UPDATE nodes SET status = 'dead'").
		WithArgs(staleBefore).
		WillReturnError(fmt.Errorf("update failed"))

	nm := NewNodeManager(mock, zerolog.Nop(), "node-1", "worker")
	updated, err := nm.MarkStaleNodesDead(context.Background(), staleBefore)
	require.Error(t, err, "MarkStaleNodesDead should return wrapped update errors")
	require.Equal(t, int64(0), updated, "MarkStaleNodesDead should return zero when the update fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeManager_BuildMetadata_MergesExtraAndProvider(t *testing.T) {
	t.Parallel()

	nm := NewNodeManager(nil, zerolog.Nop(), "node-1", "worker")
	nm.SetMetadataProvider(func() map[string]any {
		return map[string]any{"build_sha": "abc123", "active_job_count": 1}
	})

	raw, err := nm.buildMetadata(map[string]any{"active_job_count": 2, "drain_requested_at": "2026-04-21T12:00:00Z"})
	require.NoError(t, err, "buildMetadata should merge provider and extra metadata")
	require.JSONEq(t, `{"build_sha":"abc123","active_job_count":2,"drain_requested_at":"2026-04-21T12:00:00Z"}`, string(raw), "buildMetadata should let explicit extra fields override provider values")
}
