package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var threadRuntimeTestColumns = []string{
	"thread_id", "org_id", "session_id", "runtime_id", "owner_node_id", "lease_token",
	"lease_expires_at", "status", "sandbox_id", "agent_type", "model",
	"last_delivered_sequence", "last_acked_sequence", "last_heartbeat_at", "started_at", "closed_at",
}

func TestThreadRuntimeStore_UpsertLive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewThreadRuntimeStore(mock)
	now := time.Now().UTC()
	leaseToken := uuid.New()
	threadID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	sandboxID := "sandbox-123"

	mock.ExpectQuery(`INSERT INTO thread_runtimes`).
		WithArgs(anyArgs(15)...).
		WillReturnRows(
			pgxmock.NewRows(threadRuntimeTestColumns).
				AddRow(
					threadID, orgID, sessionID, "runtime-1", "worker-a", leaseToken,
					now.Add(30*time.Second), string(models.ThreadRuntimeStatusLive), &sandboxID, string(models.AgentTypeCodex), nil,
					int64(0), int64(0), now, now, nil,
				),
		)

	runtime := &models.ThreadRuntime{
		ThreadID:       threadID,
		OrgID:          orgID,
		SessionID:      sessionID,
		RuntimeID:      "runtime-1",
		OwnerNodeID:    "worker-a",
		LeaseToken:     leaseToken,
		LeaseExpiresAt: now.Add(30 * time.Second),
		Status:         models.ThreadRuntimeStatusLive,
		SandboxID:      &sandboxID,
		AgentType:      models.AgentTypeCodex,
	}

	err = store.Upsert(context.Background(), runtime)
	require.NoError(t, err, "Upsert should not return an error")
	require.Equal(t, models.ThreadRuntimeStatusLive, runtime.Status, "Upsert should round-trip the runtime status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadRuntimeStore_GetByThread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID)
		expectErr bool
	}{
		{
			name: "returns runtime row when present",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				now := time.Now().UTC()
				sessionID := uuid.New()
				mock.ExpectQuery(`SELECT .+ FROM thread_runtimes WHERE org_id = @org_id AND thread_id = @thread_id`).
					WithArgs(anyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(threadRuntimeTestColumns).
							AddRow(
								threadID, orgID, sessionID, "runtime-1", "worker-a", uuid.New(),
								now.Add(30*time.Second), string(models.ThreadRuntimeStatusLive), nil, string(models.AgentTypeClaudeCode), nil,
								int64(3), int64(2), now, now, nil,
							),
					)
			},
		},
		{
			name: "wraps query errors",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, threadID uuid.UUID) {
				mock.ExpectQuery(`SELECT .+ FROM thread_runtimes WHERE org_id = @org_id AND thread_id = @thread_id`).
					WithArgs(anyArgs(2)...).
					WillReturnError(fmt.Errorf("read failed"))
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

			store := NewThreadRuntimeStore(mock)
			orgID := uuid.New()
			threadID := uuid.New()
			tt.setupMock(mock, orgID, threadID)

			runtime, err := store.GetByThread(context.Background(), orgID, threadID)
			if tt.expectErr {
				require.Error(t, err, "GetByThread should return an error")
				return
			}
			require.NoError(t, err, "GetByThread should not return an error")
			require.Equal(t, threadID, runtime.ThreadID, "GetByThread should return the requested thread runtime")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestThreadRuntimeStore_AdvanceCursors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewThreadRuntimeStore(mock)

	mock.ExpectExec(`UPDATE thread_runtimes\s+SET last_delivered_sequence = GREATEST`).
		WithArgs(anyArgs(6)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.AdvanceCursors(context.Background(), uuid.New(), uuid.New(), "worker-a", uuid.New(), 9, 7)
	require.NoError(t, err, "AdvanceCursors should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
