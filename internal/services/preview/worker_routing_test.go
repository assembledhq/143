package preview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var workerNodeTestCols = []string{
	"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at",
}

func TestWorkerSelector_ResolveNode_AllowsDrainingWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker-1.internal:8080",
	})
	require.NoError(t, err, "should marshal worker metadata")

	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(workerNodeTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "draining", metadata, now, now),
		)

	selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
	worker, err := selector.ResolveNode(context.Background(), "worker-1")
	require.NoError(t, err, "ResolveNode should allow draining workers that still own previews")
	require.Equal(t, "worker-1", worker.ID, "ResolveNode should return the requested worker")
	require.Equal(t, "http://worker-1.internal:8080", worker.BaseURL, "ResolveNode should preserve the worker base URL")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_ResolveNode_AllowsTemporarilyNonCapableOwner(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewInternalBaseURL: "http://worker-1.internal:8080",
	})
	require.NoError(t, err, "should marshal worker metadata")

	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(workerNodeTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now),
		)

	selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
	worker, err := selector.ResolveNode(context.Background(), "worker-1")
	require.NoError(t, err, "ResolveNode should route existing previews when the worker has an internal base URL")
	require.Equal(t, "worker-1", worker.ID, "ResolveNode should return the owning worker")
	require.Equal(t, "http://worker-1.internal:8080", worker.BaseURL, "ResolveNode should preserve the worker base URL")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestParseWorkerNode(t *testing.T) {
	t.Parallel()

	t.Run("returns routable worker", func(t *testing.T) {
		t.Parallel()

		metadata, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080/",
		})
		require.NoError(t, err, "worker metadata should marshal")

		worker, err := parseWorkerNode(models.Node{
			ID:       "worker-1",
			Mode:     "worker",
			Metadata: metadata,
		})
		require.NoError(t, err, "parseWorkerNode should accept preview-capable workers")
		require.Equal(t, "http://worker-1.internal:8080", worker.BaseURL, "parseWorkerNode should trim trailing slashes")
	})

	t.Run("rejects invalid metadata", func(t *testing.T) {
		t.Parallel()

		_, err := parseWorkerNode(models.Node{
			ID:       "worker-1",
			Metadata: []byte("{"),
		})
		require.Error(t, err, "parseWorkerNode should reject malformed metadata")
		require.Contains(t, err.Error(), "parse node metadata", "parseWorkerNode should wrap metadata parse failures")
	})

	t.Run("rejects non preview capable workers", func(t *testing.T) {
		t.Parallel()

		_, err := parseWorkerNode(models.Node{ID: "worker-1"})
		require.Error(t, err, "parseWorkerNode should reject workers without preview capability")
		require.Contains(t, err.Error(), "not preview-capable", "parseWorkerNode should explain why the worker is rejected")
	})

	t.Run("rejects missing base url", func(t *testing.T) {
		t.Parallel()

		metadata, err := json.Marshal(WorkerNodeMetadata{PreviewCapable: true})
		require.NoError(t, err, "worker metadata should marshal")

		_, err = parseWorkerNode(models.Node{
			ID:       "worker-1",
			Metadata: metadata,
		})
		require.Error(t, err, "parseWorkerNode should reject preview-capable workers without a base URL")
		require.Contains(t, err.Error(), "has no preview internal base url", "parseWorkerNode should explain missing base URLs")
	})
}

func TestWorkerSelector_ResolveNode_RejectsUnroutableStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker-1.internal:8080",
	})
	require.NoError(t, err, "should marshal worker metadata")

	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(workerNodeTestCols).
				AddRow("worker-1", "worker", "worker-1.internal", "dead", metadata, now, now),
		)

	selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
	_, err = selector.ResolveNode(context.Background(), "worker-1")
	require.Error(t, err, "ResolveNode should reject unroutable workers")
	require.Contains(t, err.Error(), "not routable", "ResolveNode should explain why the worker was rejected")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectStartNode(t *testing.T) {
	t.Parallel()

	t.Run("requires a session", func(t *testing.T) {
		t.Parallel()

		selector := NewWorkerSelector(nil, nil)
		_, err := selector.SelectStartNode(context.Background(), uuid.New(), nil)
		require.Error(t, err, "SelectStartNode should reject nil sessions")
		require.Contains(t, err.Error(), "session is required", "SelectStartNode should explain missing sessions")
	})

	t.Run("routes existing preview to owning worker", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		orgID := uuid.New()
		userID := uuid.New()
		sessionID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()
		metadata, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080",
		})
		require.NoError(t, err, "should marshal worker metadata")

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(previewInstanceTestCols).
					AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
			)
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now),
			)

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		worker, err := selector.SelectStartNode(context.Background(), orgID, &models.Session{ID: sessionID})
		require.NoError(t, err, "SelectStartNode should route existing previews to their owner")
		require.Equal(t, "worker-1", worker.ID, "SelectStartNode should return the owning worker")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps active preview lookup errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		_, err = selector.SelectStartNode(context.Background(), orgID, &models.Session{ID: sessionID})
		require.Error(t, err, "SelectStartNode should surface preview lookup failures")
		require.Contains(t, err.Error(), "lookup active preview", "SelectStartNode should wrap preview lookup failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("rejects legacy live sessions without worker ownership", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		containerID := "container-1"

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		_, err = selector.SelectStartNode(context.Background(), orgID, &models.Session{
			ID:           sessionID,
			ContainerID:  &containerID,
			SandboxState: models.SandboxStateRunning,
		})
		require.ErrorIs(t, err, ErrLegacySessionWorkerOwnership, "SelectStartNode should fail closed for legacy live sessions")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestWorkerSelector_SelectLeastLoadedNodeExcept(t *testing.T) {
	t.Parallel()

	t.Run("chooses least loaded worker and skips excluded nodes", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		now := time.Now().UTC()
		workerOneMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080",
		})
		require.NoError(t, err, "should marshal first worker metadata")
		workerTwoMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-2.internal:8080",
		})
		require.NoError(t, err, "should marshal second worker metadata")
		apiMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://api.internal:8080",
		})
		require.NoError(t, err, "should marshal api metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("api-1", "api", "api.internal", "active", apiMeta, now, now).
					AddRow("worker-1", "worker", "worker-1.internal", "active", workerOneMeta, now, now).
					AddRow("worker-2", "worker", "worker-2.internal", "active", workerTwoMeta, now, now),
			)
		// Single batch query replaces the previous N sequential COUNT queries.
		mock.ExpectQuery("SELECT worker_node_id, COUNT").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
				AddRow("worker-1", 3).
				AddRow("worker-2", 1))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		worker, err := selector.SelectLeastLoadedNodeExcept(context.Background(), map[string]struct{}{"api-1": {}})
		require.NoError(t, err, "SelectLeastLoadedNodeExcept should pick the least loaded eligible worker")
		require.Equal(t, "worker-2", worker.ID, "SelectLeastLoadedNodeExcept should pick the least loaded eligible worker")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns no preview workers when none are eligible", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		now := time.Now().UTC()
		apiMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://api.internal:8080",
		})
		require.NoError(t, err, "should marshal api metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("api-1", "api", "api.internal", "active", apiMeta, now, now),
			)

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		_, err = selector.SelectLeastLoadedNode(context.Background())
		require.ErrorIs(t, err, ErrNoPreviewWorkers, "SelectLeastLoadedNode should fail when no preview workers are available")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps worker count errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		now := time.Now().UTC()
		workerMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080",
		})
		require.NoError(t, err, "should marshal worker metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", workerMeta, now, now),
			)
		mock.ExpectQuery("SELECT worker_node_id, COUNT").
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		_, err = selector.SelectLeastLoadedNode(context.Background())
		require.Error(t, err, "SelectLeastLoadedNode should surface count failures")
		require.Contains(t, err.Error(), "count active previews", "SelectLeastLoadedNode should wrap count failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestWorkerSelector_SelectCachePlacementWorkerBatchesCapacityChecks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	now := time.Now().UTC()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker.internal:8080",
	})
	require.NoError(t, err, "worker metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, "cache-key", "placement", "worker-1", int64(10), now, now).
			AddRow(uuid.New(), orgID, repoID, "cache-key", "placement", "worker-2", int64(10), now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now).
			AddRow("worker-2", "worker", "worker-2.internal", "active", metadata, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("worker-1", 2).
			AddRow("worker-2", 0))

	selector := NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), db.NewPreviewStore(mock), 2)
	worker, ok, err := selector.selectCachePlacementWorker(context.Background(), orgID, repoID, "placement", true)
	require.NoError(t, err, "cache placement worker lookup should not fail")
	require.True(t, ok, "cache placement worker lookup should find a candidate")
	require.Equal(t, "worker-2", worker.ID, "cache placement worker should skip full cache holders using one batched count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectLeastLoadedNodeInPreferredRegionIgnoresUnknownRegion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now().UTC()
	unknownMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://unknown.internal:8080",
	})
	require.NoError(t, err, "unknown-region metadata should marshal")
	westMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://west.internal:8080",
		Region:                 "us-west-2",
	})
	require.NoError(t, err, "west metadata should marshal")
	eastMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://east.internal:8080",
		Region:                 "us-east-1",
	})
	require.NoError(t, err, "east metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("unknown-worker", "worker", "unknown.internal", "active", unknownMeta, now, now).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now).
			AddRow("east-worker", "worker", "east.internal", "active", eastMeta, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("east-worker", 0))

	selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
		MaxPreviewsPerWorker: 10,
		PreferredRegion:      "us-east-1",
	})
	worker, err := selector.SelectLeastLoadedNodeInPreferredRegion(context.Background())
	require.NoError(t, err, "preferred-region selection should find the explicitly matching worker")
	require.Equal(t, "east-worker", worker.ID, "preferred-region selection should not treat workers with unknown regions as matching")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectStartNodeWithPlacementPrefersRegionThenCrossRegion(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	eastMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://east.internal:8080",
		Region:                 "us-east-1",
	})
	require.NoError(t, err, "east metadata should marshal")
	westMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://west.internal:8080",
		Region:                 "us-west-2",
	})
	require.NoError(t, err, "west metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, "cache-key", "placement", "west-worker", int64(10), now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("east-worker", "worker", "east.internal", "active", eastMeta, now, now).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("east-worker", "worker", "east.internal", "active", eastMeta, now, now).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("east-worker", 3))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("east-worker", "worker", "east.internal", "active", eastMeta, now, now).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("east-worker", 3))
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, "cache-key", "placement", "west-worker", int64(10), now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("east-worker", "worker", "east.internal", "active", eastMeta, now, now).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("west-worker", 0))

	selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
		MaxPreviewsPerWorker: 3,
		PreferredRegion:      "us-east-1",
	})
	worker, err := selector.SelectStartNodeWithPlacement(context.Background(), orgID, &models.Session{ID: sessionID}, repoID, "placement")
	require.NoError(t, err, "worker selection should fall back across regions when preferred region is full")
	require.Equal(t, "west-worker", worker.ID, "cross-region fallback should pick a healthy worker only after preferred-region candidates are full")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestWorkerSelector_SelectStartNodeWithPlacement_NoPreferredRegionWorkers verifies that
// when no workers exist in the preferred region at all, routing falls through to the
// cross-region least-loaded fallback instead of returning ErrNoPreviewWorkers.
func TestWorkerSelector_SelectStartNodeWithPlacement_NoPreferredRegionWorkers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()

	// Only a west worker exists; preferred region is east.
	westMeta, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://west.internal:8080",
		Region:                 "us-west-2",
	})
	require.NoError(t, err, "west metadata should marshal")

	// 1. No active preview for the session.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	// 2. selectCachePlacementWorker(preferredOnly=true): no location hints.
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}))

	// 3. selectRendezvousWorker(preferredOnly=true): only west workers — no east workers eligible.
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))

	// 4. SelectLeastLoadedNodeInPreferredRegion: no preferred-region workers.
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))

	// 5. Cross-region: selectCachePlacementWorker(preferredOnly=false): no location hints.
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}))

	// 6. Cross-region: selectRendezvousWorker(preferredOnly=false): west worker is eligible, at capacity=0.
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("west-worker", "worker", "west.internal", "active", westMeta, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("west-worker", 0))

	selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
		MaxPreviewsPerWorker: 3,
		PreferredRegion:      "us-east-1",
	})
	worker, err := selector.SelectStartNodeWithPlacement(context.Background(), orgID, &models.Session{ID: sessionID}, repoID, "placement")
	require.NoError(t, err, "routing should fall through to cross-region when no preferred-region workers exist")
	require.Equal(t, "west-worker", worker.ID, "should route to the only available worker in a different region")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
