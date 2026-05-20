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
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id = @worker").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id = @worker").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

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

	t.Run("requires static egress capable worker when requested", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		now := time.Now().UTC()
		directOnlyMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080",
		})
		require.NoError(t, err, "should marshal direct worker metadata")
		staticMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-2.internal:8080",
			StaticEgressCapable:    true,
		})
		require.NoError(t, err, "should marshal static egress worker metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", directOnlyMeta, now, now).
					AddRow("worker-2", "worker", "worker-2.internal", "active", staticMeta, now, now),
			)
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id = @worker").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		worker, err := selector.SelectLeastLoadedNodeWithRequirements(context.Background(), WorkerSelectionRequirements{StaticEgressRequired: true})
		require.NoError(t, err, "SelectLeastLoadedNodeWithRequirements should find a static egress capable worker")
		require.Equal(t, "worker-2", worker.ID, "selection should skip preview workers that cannot serve static egress sandboxes")
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
		mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM preview_instances WHERE worker_node_id = @worker").
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		_, err = selector.SelectLeastLoadedNode(context.Background())
		require.Error(t, err, "SelectLeastLoadedNode should surface count failures")
		require.Contains(t, err.Error(), "count active previews", "SelectLeastLoadedNode should wrap count failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}
