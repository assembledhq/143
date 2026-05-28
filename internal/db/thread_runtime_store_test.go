package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var threadRuntimeTestColumns = []string{
	"id", "org_id", "session_id", "thread_id", "sandbox_id", "container_id",
	"runtime_handle_id", "agent_type", "model", "status", "owner_node_id",
	"lease_token", "last_delivered_sequence", "last_acked_sequence",
	"base_workspace_generation", "current_workspace_generation",
	"started_at", "heartbeat_at", "lease_expires_at", "closed_at",
	"stop_reason", "last_error", "created_at", "updated_at",
}

func TestThreadRuntimeStore_CreateStarting(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery(`WITH expired AS \(\s*UPDATE thread_runtimes[\s\S]+status = 'lost'[\s\S]+expired_holders AS[\s\S]+reset_inbox AS[\s\S]+unknown_inbox AS[\s\S]+INSERT INTO thread_runtimes`).
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(threadRuntimeTestColumns).AddRow(
			runtimeID, orgID, sessionID, threadID, uuid.Nil, "container-1",
			"exec-1", models.AgentTypeCodex, "gpt-5", models.ThreadRuntimeStatusStarting,
			"worker-1", leaseToken, int64(0), int64(0), int64(1), int64(1),
			now, now, now.Add(time.Minute), nil, nil, nil, now, now,
		))

	store := NewThreadRuntimeStore(mock)
	runtime, err := store.CreateStarting(context.Background(), orgID, CreateThreadRuntimeParams{
		SessionID:                  sessionID,
		ThreadID:                   threadID,
		ContainerID:                "container-1",
		RuntimeHandleID:            "exec-1",
		AgentType:                  models.AgentTypeCodex,
		Model:                      "gpt-5",
		OwnerNodeID:                "worker-1",
		LeaseToken:                 leaseToken,
		BaseWorkspaceGeneration:    1,
		CurrentWorkspaceGeneration: 1,
		LeaseDuration:              time.Minute,
	})

	require.NoError(t, err, "CreateStarting should not return an error")
	require.Equal(t, runtimeID, runtime.ID, "CreateStarting should return inserted runtime")
	require.Equal(t, models.ThreadRuntimeStatusStarting, runtime.Status, "CreateStarting should create a starting runtime")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadRuntimeStore_AdvanceDeliveryWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	runtimeID := uuid.New()
	leaseToken := uuid.New()

	mock.ExpectExec("UPDATE thread_runtimes").
		WithArgs(orgID, runtimeID, leaseToken, int64(8), int64(6)).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewThreadRuntimeStore(mock)
	updated, err := store.AdvanceDeliveryWithLease(context.Background(), orgID, runtimeID, leaseToken, 8, 6)

	require.NoError(t, err, "AdvanceDeliveryWithLease should not return an error")
	require.True(t, updated, "AdvanceDeliveryWithLease should report a matching lease update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadRuntimeStore_GetActiveByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	runtimeID := uuid.New()
	sandboxID := uuid.New()
	leaseToken := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT .* FROM thread_runtimes").
		WithArgs(orgID, threadID).
		WillReturnRows(pgxmock.NewRows(threadRuntimeTestColumns).AddRow(
			runtimeID, orgID, sessionID, threadID, sandboxID, "container-1",
			"handle-1", models.AgentTypeCodex, "gpt-5", models.ThreadRuntimeStatusLive,
			"worker-1", leaseToken, int64(4), int64(3), int64(0), int64(1),
			now, now, now.Add(time.Minute), nil, nil, nil, now, now,
		))

	store := NewThreadRuntimeStore(mock)
	runtime, err := store.GetActiveByThread(context.Background(), orgID, threadID)

	require.NoError(t, err, "GetActiveByThread should not return an error")
	require.Equal(t, runtimeID, runtime.ID, "GetActiveByThread should return the active runtime row")
	require.Equal(t, int64(4), runtime.LastDeliveredSequence, "GetActiveByThread should preserve delivery cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestThreadRuntimeStore_ReclaimExpiredLeases(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should be created")
	defer mock.Close()

	cutoff := time.Now().UTC()

	mock.ExpectQuery(`WITH candidates AS \(\s*SELECT[\s\S]+lost_runtimes AS \(\s*UPDATE thread_runtimes[\s\S]+session_sandbox_holders[\s\S]+thread_inbox_entries`).
		WithArgs(cutoff, 25).
		WillReturnRows(pgxmock.NewRows([]string{
			"lost_runtime_count", "expired_holder_count", "reset_inbox_count", "unknown_inbox_count",
		}).AddRow(int64(2), int64(2), int64(3), int64(1)))

	store := NewThreadRuntimeStore(mock)
	result, err := store.ReclaimExpiredLeases(context.Background(), cutoff, 25)

	require.NoError(t, err, "ReclaimExpiredLeases should not return an error")
	require.Equal(t, ThreadRuntimeReclaimResult{LostRuntimes: 2, ExpiredHolders: 2, ResetInboxEntries: 3, UnknownDeliveryEntries: 1}, result, "ReclaimExpiredLeases should return affected row counts")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
