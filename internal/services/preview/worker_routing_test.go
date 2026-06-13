package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
			StaticEgressPublicIP:   "203.0.113.10",
		})
		require.NoError(t, err, "should marshal static egress worker metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", directOnlyMeta, now, now).
					AddRow("worker-2", "worker", "worker-2.internal", "active", staticMeta, now, now),
			)
		mock.ExpectQuery("SELECT worker_node_id, COUNT").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		worker, err := selector.SelectLeastLoadedNodeWithRequirements(context.Background(), WorkerSelectionRequirements{
			StaticEgressRequired: true,
			StaticEgressPublicIP: "203.0.113.10",
		})
		require.NoError(t, err, "SelectLeastLoadedNodeWithRequirements should find a static egress capable worker")
		require.Equal(t, "worker-2", worker.ID, "selection should skip preview workers that cannot serve static egress sandboxes")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("requires static egress public ip to match the configured gateway", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()

		now := time.Now().UTC()
		staleMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-1.internal:8080",
			StaticEgressCapable:    true,
			StaticEgressPublicIP:   "198.51.100.20",
		})
		require.NoError(t, err, "should marshal stale static egress worker metadata")
		currentMeta, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: "http://worker-2.internal:8080",
			StaticEgressCapable:    true,
			StaticEgressPublicIP:   "203.0.113.10",
		})
		require.NoError(t, err, "should marshal current static egress worker metadata")

		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(
				pgxmock.NewRows(workerNodeTestCols).
					AddRow("worker-1", "worker", "worker-1.internal", "active", staleMeta, now, now).
					AddRow("worker-2", "worker", "worker-2.internal", "active", currentMeta, now, now),
			)
		mock.ExpectQuery("SELECT worker_node_id, COUNT").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))

		selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
		worker, err := selector.SelectLeastLoadedNodeWithRequirements(context.Background(), WorkerSelectionRequirements{
			StaticEgressRequired: true,
			StaticEgressPublicIP: "203.0.113.10",
		})
		require.NoError(t, err, "SelectLeastLoadedNodeWithRequirements should find a worker verified against the configured public IP")
		require.Equal(t, "worker-2", worker.ID, "selection should skip workers verified against a stale static egress public IP")
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

func TestWorkerSelector_HasStaticEgressCapableWorker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata []WorkerNodeMetadata
		expected bool
	}{
		{
			name: "returns true when all active session workers advertise static egress",
			metadata: []WorkerNodeMetadata{
				{
					StaticEgressCapable:  true,
					StaticEgressPublicIP: "203.0.113.10",
				},
			},
			expected: true,
		},
		{
			name: "returns false when static egress public ip is stale",
			metadata: []WorkerNodeMetadata{
				{
					PreviewCapable:         true,
					PreviewInternalBaseURL: "http://worker-1.internal:8080",
					StaticEgressCapable:    true,
					StaticEgressPublicIP:   "198.51.100.20",
				},
			},
			expected: false,
		},
		{
			name: "returns false when active workers are not static egress capable",
			metadata: []WorkerNodeMetadata{
				{
					PreviewCapable:         true,
					PreviewInternalBaseURL: "http://worker-1.internal:8080",
				},
			},
			expected: false,
		},
		{
			name: "returns false when only some active session workers advertise static egress",
			metadata: []WorkerNodeMetadata{
				{
					StaticEgressCapable:  true,
					StaticEgressPublicIP: "203.0.113.10",
				},
				{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			now := time.Now().UTC()
			rows := pgxmock.NewRows(workerNodeTestCols)
			for i, item := range tt.metadata {
				raw, err := json.Marshal(item)
				require.NoError(t, err, "should marshal worker metadata")
				rows.AddRow(fmt.Sprintf("worker-%d", i+1), "worker", fmt.Sprintf("worker-%d.internal", i+1), "active", raw, now, now)
			}
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
				WillReturnRows(rows)

			selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
			ok, err := selector.HasStaticEgressCapableWorker(context.Background(), "203.0.113.10")
			require.NoError(t, err, "HasStaticEgressCapableWorker should not error when listing active nodes succeeds")
			require.Equal(t, tt.expected, ok, "HasStaticEgressCapableWorker should report whether all active session workers can serve static egress")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorkerSelector_StaticEgressWorkerDiagnosticsIdentifiesMismatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		nodes []struct {
			mode     string
			metadata WorkerNodeMetadata
		}
		expected StaticEgressWorkerDiagnostics
	}{
		{
			name: "reports missing capability and stale public ip",
			nodes: []struct {
				mode     string
				metadata WorkerNodeMetadata
			}{
				{
					mode:     "worker",
					metadata: WorkerNodeMetadata{},
				},
				{
					mode: "worker",
					metadata: WorkerNodeMetadata{
						StaticEgressCapable:  true,
						StaticEgressPublicIP: "198.51.100.20",
					},
				},
				{
					mode: "api",
					metadata: WorkerNodeMetadata{
						StaticEgressCapable:  false,
						StaticEgressPublicIP: "",
					},
				},
			},
			expected: StaticEgressWorkerDiagnostics{
				Available: false,
				Mismatches: []StaticEgressWorkerMismatch{
					{
						NodeID:               "worker-1",
						Host:                 "worker-1.internal",
						Mode:                 "worker",
						StaticEgressCapable:  false,
						StaticEgressPublicIP: "",
						Reason:               "missing static egress capability",
					},
					{
						NodeID:               "worker-2",
						Host:                 "worker-2.internal",
						Mode:                 "worker",
						StaticEgressCapable:  true,
						StaticEgressPublicIP: "198.51.100.20",
						Reason:               "static egress public IP mismatch",
					},
				},
			},
		},
		{
			name: "reports no active session workers",
			nodes: []struct {
				mode     string
				metadata WorkerNodeMetadata
			}{
				{mode: "api", metadata: WorkerNodeMetadata{}},
			},
			expected: StaticEgressWorkerDiagnostics{
				Available: false,
				Mismatches: []StaticEgressWorkerMismatch{
					{
						Reason: "no active session workers",
					},
				},
			},
		},
		{
			name: "available when every session worker matches",
			nodes: []struct {
				mode     string
				metadata WorkerNodeMetadata
			}{
				{
					mode: "worker",
					metadata: WorkerNodeMetadata{
						StaticEgressCapable:  true,
						StaticEgressPublicIP: "203.0.113.10",
					},
				},
			},
			expected: StaticEgressWorkerDiagnostics{Available: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			now := time.Now().UTC()
			rows := pgxmock.NewRows(workerNodeTestCols)
			for i, item := range tt.nodes {
				raw, err := json.Marshal(item.metadata)
				require.NoError(t, err, "should marshal worker metadata")
				rows.AddRow(fmt.Sprintf("worker-%d", i+1), item.mode, fmt.Sprintf("worker-%d.internal", i+1), "active", raw, now, now)
			}
			mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
				WillReturnRows(rows)

			selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
			got, err := selector.StaticEgressWorkerDiagnostics(context.Background(), "203.0.113.10")
			require.NoError(t, err, "StaticEgressWorkerDiagnostics should not error when listing active nodes succeeds")
			require.Equal(t, tt.expected, got, "StaticEgressWorkerDiagnostics should identify static egress blockers")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorkerSelector_StaticEgressWorkerDiagnosticsEmptyPublicIP(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now().UTC()
	raw, err := json.Marshal(WorkerNodeMetadata{StaticEgressCapable: true, StaticEgressPublicIP: ""})
	require.NoError(t, err, "should marshal worker metadata")
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("worker-1", "worker", "worker-1.internal", "active", raw, now, now))

	selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
	got, err := selector.StaticEgressWorkerDiagnostics(context.Background(), "")
	require.NoError(t, err, "StaticEgressWorkerDiagnostics should not error")
	require.False(t, got.Available, "should be unavailable when configured public IP is empty")
	require.Len(t, got.Mismatches, 1, "should report one mismatch")
	require.Equal(t, "public IP not configured", got.Mismatches[0].Reason, "should use fallback reason when public IP is empty")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "worker-1", int64(10), now, now).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "worker-2", int64(10), now, now))
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
	worker, ok, err := selector.selectCachePlacementWorker(context.Background(), orgID, repoID, []WorkerCachePlacement{{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: "placement"}}, true, WorkerSelectionRequirements{})
	require.NoError(t, err, "cache placement worker lookup should not fail")
	require.True(t, ok, "cache placement worker lookup should find a candidate")
	require.Equal(t, "worker-2", worker.ID, "cache placement worker should skip full cache holders using one batched count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectCachePlacementWorkerChoosesLeastLoadedHolder(t *testing.T) {
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "worker-busy", int64(10), now, now).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "worker-idle", int64(10), now.Add(-time.Minute), now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("worker-busy", "worker", "worker-busy.internal", "active", metadata, now, now).
			AddRow("worker-idle", "worker", "worker-idle.internal", "active", metadata, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("worker-busy", 4).
			AddRow("worker-idle", 1))

	selector := NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), db.NewPreviewStore(mock), 10)
	worker, ok, err := selector.selectCachePlacementWorker(context.Background(), orgID, repoID, []WorkerCachePlacement{{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: "placement"}}, true, WorkerSelectionRequirements{})
	require.NoError(t, err, "cache placement worker lookup should not fail")
	require.True(t, ok, "cache placement worker lookup should find a candidate")
	require.Equal(t, "worker-idle", worker.ID, "cache placement should prefer the least-loaded holder instead of the newest hint when both have the blob")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectStartNodeWithCachePlacementsUsesPackageManagerHolder(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker.internal:8080",
	})
	require.NoError(t, err, "worker metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), models.PreviewCacheKindInstallArtifact, "install-placement", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), models.PreviewCacheKindPackageManager, "pm-placement", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindPackageManager, "pm-cache-key", "pm-placement", "worker-pm", int64(10), now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("worker-pm", "worker", "worker-pm.internal", "active", metadata, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("worker-pm", 0))

	selector := NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), db.NewPreviewStore(mock), 2)
	worker, err := selector.SelectStartNodeWithCachePlacementsAndRequirements(context.Background(), orgID, &models.Session{ID: sessionID}, repoID, []WorkerCachePlacement{
		{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: "install-placement"},
		{Kind: models.PreviewCacheKindPackageManager, PlacementKey: "pm-placement"},
	}, WorkerSelectionRequirements{})

	require.NoError(t, err, "selection should use package-manager cache placement when install placement misses")
	require.Equal(t, "worker-pm", worker.ID, "worker with the package-manager L1 cache should be selected")
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

func TestWorkerSelector_SelectStartNodeFallsBackToRecentRepoCacheHolder(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	now := time.Now().UTC()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker-warm.internal:8080",
	})
	require.NoError(t, err, "worker metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), models.PreviewCacheKindInstallArtifact, "missing-placement", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}))
	mock.ExpectQuery("SELECT .+ FROM preview_dependency_cache_locations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "other-placement", "worker-warm", int64(10), now, now))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).
			AddRow("worker-warm", "worker", "worker-warm.internal", "active", metadata, now, now))
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}).
			AddRow("worker-warm", 0))

	selector := NewWorkerSelectorWithMaxPerWorker(db.NewNodeStore(mock), db.NewPreviewStore(mock), 2)
	worker, err := selector.SelectStartNodeWithCachePlacementsAndRequirements(context.Background(), orgID, &models.Session{ID: sessionID}, repoID, []WorkerCachePlacement{
		{Kind: models.PreviewCacheKindInstallArtifact, PlacementKey: "missing-placement", Approximate: true},
	}, WorkerSelectionRequirements{})
	require.NoError(t, err, "SelectStartNode should fall back to a recent repo cache holder")
	require.Equal(t, "worker-warm", worker.ID, "recent repo cache holder should beat cold rendezvous when exact placement has no holder")
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "west-worker", int64(10), now, now))
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
		}).
			AddRow(uuid.New(), orgID, repoID, models.PreviewCacheKindInstallArtifact, "cache-key", "placement", "west-worker", int64(10), now, now))
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "repo_id", "cache_kind", "cache_key", "placement_key", "worker_node_id", "size_bytes", "last_used_at", "created_at",
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
