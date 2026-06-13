package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var nodeStoreTestCols = []string{
	"id", "mode", "host", "status", "drain_intent", "metadata", "started_at", "last_heartbeat_at",
	"drain_requested_at", "drain_budget_expires_at", "drain_requested_by", "drain_reason",
}

type timestamptzArg struct{}

func (timestamptzArg) Match(v interface{}) bool {
	_, ok := v.(pgtype.Timestamptz)
	return ok
}

func TestNodeStore_GetByID(t *testing.T) {
	t.Parallel()

	t.Run("returns node", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		now := time.Now().UTC()
		metadata, err := json.Marshal(map[string]any{"preview_capable": true})
		require.NoError(t, err, "metadata should marshal")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(nodeStoreTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", "none", metadata, now, now, nil, nil, "", ""),
			)

		store := NewNodeStore(mock)
		node, err := store.GetByID(context.Background(), "worker-1")
		require.NoError(t, err, "GetByID should return the node")
		require.NotNil(t, node, "GetByID should return a non-nil node")
		require.Equal(t, "worker-1", node.ID, "GetByID should preserve the node id")
		require.Equal(t, metadata, []byte(node.Metadata), "GetByID should preserve metadata")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps query errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		store := NewNodeStore(mock)
		_, err = store.GetByID(context.Background(), "worker-1")
		require.Error(t, err, "GetByID should surface query failures")
		require.Contains(t, err.Error(), "query node", "GetByID should wrap query failures with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps scan errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(nodeStoreTestCols))

		store := NewNodeStore(mock)
		_, err = store.GetByID(context.Background(), "missing-worker")
		require.Error(t, err, "GetByID should surface scan failures")
		require.ErrorIs(t, err, pgx.ErrNoRows, "GetByID should preserve pgx.ErrNoRows")
		require.Contains(t, err.Error(), "get node", "GetByID should wrap scan failures with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestNodeStore_ListActive(t *testing.T) {
	t.Parallel()

	t.Run("returns active nodes", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		now := time.Now().UTC()
		firstMeta, err := json.Marshal(map[string]any{"preview_capable": true})
		require.NoError(t, err, "first metadata should marshal")
		secondMeta, err := json.Marshal(map[string]any{"preview_capable": false})
		require.NoError(t, err, "second metadata should marshal")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(nodeStoreTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", "none", firstMeta, now, now, nil, nil, "", "").
					AddRow("worker-2", "api", "api-1.internal", "active", "none", secondMeta, now, now, nil, nil, "", ""),
			)

		store := NewNodeStore(mock)
		nodes, err := store.ListActive(context.Background())
		require.NoError(t, err, "ListActive should return nodes")
		require.Len(t, nodes, 2, "ListActive should return every active node")
		require.Equal(t, "worker-1", nodes[0].ID, "ListActive should preserve row order")
		require.Equal(t, models.NodeModeAPI, nodes[1].Mode, "ListActive should decode node modes")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps query errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnError(errors.New("db unavailable"))

		store := NewNodeStore(mock)
		_, err = store.ListActive(context.Background())
		require.Error(t, err, "ListActive should surface query failures")
		require.Contains(t, err.Error(), "list active nodes", "ListActive should wrap query failures with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps scan errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		now := time.Now().UTC()
		metadata, err := json.Marshal(map[string]any{"preview_capable": true})
		require.NoError(t, err, "metadata should marshal")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(nodeStoreTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", "none", metadata, "not-a-time", now, nil, nil, "", ""),
			)

		store := NewNodeStore(mock)
		_, err = store.ListActive(context.Background())
		require.Error(t, err, "ListActive should surface scan failures")
		require.Contains(t, err.Error(), "scan active nodes", "ListActive should wrap scan failures with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestNodeStore_ListPreviewRPCProbeNodesRequiresAuthCheckCapability(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	metadata, err := json.Marshal(map[string]any{
		"preview_capable":           true,
		"preview_rpc_auth_check":    true,
		"preview_internal_base_url": "http://worker-1:8080",
	})
	require.NoError(t, err, "metadata should marshal")

	mock.ExpectQuery(`SELECT .+ FROM nodes[\s\S]+metadata->>'preview_rpc_auth_check' = 'true'[\s\S]+ORDER BY id ASC`).
		WillReturnRows(
			pgxmock.NewRows(nodeStoreTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "active", "none", metadata, now, now, nil, nil, "", ""),
		)

	store := NewNodeStore(mock)
	nodes, err := store.ListPreviewRPCProbeNodes(context.Background())
	require.NoError(t, err, "ListPreviewRPCProbeNodes should return probe-capable nodes")
	require.Equal(t, []models.Node{{
		ID:                   "worker-1",
		Mode:                 models.NodeModeWorker,
		Host:                 "worker-1.internal",
		Status:               models.NodeStatusActive,
		DrainIntent:          models.DrainIntentNone,
		Metadata:             metadata,
		StartedAt:            now,
		LastHeartbeatAt:      now,
		DrainRequestedBy:     "",
		DrainReason:          "",
		DrainRequestedAt:     nil,
		DrainBudgetExpiresAt: nil,
	}}, nodes, "ListPreviewRPCProbeNodes should preserve matching node rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeStore_WorkerHeartbeatHealth(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	staleBefore := time.Now().UTC().Add(-2 * time.Minute)
	mock.ExpectQuery("SELECT[\\s\\S]+COUNT\\(\\*\\) FILTER[\\s\\S]+FROM nodes").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"active_workers", "fresh_workers", "stale_workers", "newest_heartbeat_age_seconds"}).
				AddRow(int64(3), int64(1), int64(2), float64(125)),
		)

	store := NewNodeStore(mock)
	health, err := store.WorkerHeartbeatHealth(context.Background(), staleBefore)
	require.NoError(t, err, "WorkerHeartbeatHealth should return heartbeat health")
	require.Equal(t, WorkerHeartbeatHealth{
		ActiveWorkers:             3,
		FreshWorkers:              1,
		StaleWorkers:              2,
		NewestHeartbeatAgeSeconds: 125,
	}, health, "WorkerHeartbeatHealth should scan the aggregate counts")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeStore_WorkerDeployStatusCountsDetachedRetireBlockers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	deadline := now.Add(30 * time.Minute)
	mock.ExpectQuery("WITH node_row AS").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "host", "status", "drain_intent", "last_heartbeat_at",
				"drain_requested_at", "drain_budget_expires_at",
				"active_executor_count", "max_deadline_at",
				"active_preview_count", "max_lease_expires_at",
				"running_count", "active_session_hold_count", "active_sandbox_holder_count",
				"endpoint_blocker_count", "pending_snapshot_upload_count", "detached_cleanup_job_count",
			}).AddRow(
				"worker-1", "worker.internal", "draining", "planned_rollout", now,
				now.Add(-time.Minute), deadline,
				int64(0), nil,
				int64(0), nil,
				int64(0), int64(0), int64(0),
				int64(0), int64(1), int64(2),
			),
		)

	store := NewNodeStore(mock)
	status, err := store.WorkerDeployStatus(context.Background(), "worker-1")
	require.NoError(t, err, "WorkerDeployStatus should return status with detached blockers")
	require.Equal(t, int64(1), status.PendingSnapshotUploadCount, "pending snapshot uploads should be counted as retire blockers")
	require.Equal(t, int64(2), status.DetachedCleanupJobCount, "detached cleanup jobs should be counted as retire blockers")
	require.False(t, status.RetireReady, "retire readiness should stay false while detached work exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeStore_WorkerDeployImpactListsRuntimeIdentities(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	executorID := uuid.New()
	jobID := uuid.New()
	runtimeID := uuid.New()
	expires := time.Now().UTC().Add(10 * time.Minute)

	mock.ExpectQuery("WITH active_executors AS").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"kind", "org_id", "session_id", "thread_id", "runtime_id", "job_id", "status", "deadline_at", "endpoint_url", "reason"}).
				AddRow("executor", orgID, pgtype.UUID{Bytes: sessionID, Valid: true}, pgtype.UUID{Bytes: threadID, Valid: true}, executorID, pgtype.UUID{Bytes: jobID, Valid: true}, "running", expires, "", "").
				AddRow("preview", orgID, pgtype.UUID{Bytes: sessionID, Valid: true}, pgtype.UUID{}, runtimeID, pgtype.UUID{}, "ready", expires, "http://10.0.0.5:8080", "preview-1"),
		)

	store := NewNodeStore(mock)
	impact, err := store.WorkerDeployImpact(context.Background(), "worker-1")
	require.NoError(t, err, "WorkerDeployImpact should return runtime identities")
	require.Equal(t, []WorkerDeployImpactItem{
		{
			Kind:       "executor",
			OrgID:      orgID,
			SessionID:  &sessionID,
			ThreadID:   &threadID,
			RuntimeID:  executorID,
			JobID:      &jobID,
			Status:     "running",
			DeadlineAt: &expires,
		},
		{
			Kind:        "preview",
			OrgID:       orgID,
			SessionID:   &sessionID,
			RuntimeID:   runtimeID,
			Status:      "ready",
			DeadlineAt:  &expires,
			EndpointURL: "http://10.0.0.5:8080",
			Reason:      "preview-1",
		},
	}, impact.Items, "WorkerDeployImpact should preserve affected runtime identities")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestNodeStore_RetainActiveExecutorImagesCastsExpiry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mock.ExpectExec("INSERT INTO worker_image_retention[\\s\\S]+CAST\\(@expires_at AS timestamptz\\)[\\s\\S]+FROM session_executors").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), timestamptzArg{}, pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 2))

	store := NewNodeStore(mock)
	count, err := store.RetainActiveExecutorImages(context.Background(), RetainWorkerImagesParams{
		NodeID:    "worker-1",
		DeployID:  "deploy-1",
		Reason:    "test retention",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	require.NoError(t, err, "RetainActiveExecutorImages should retain active image rows")
	require.Equal(t, int64(2), count, "RetainActiveExecutorImages should return inserted row count")
	require.NoError(t, mock.ExpectationsWereMet(), "retention insert should cast expires_at for Postgres type inference")
}
