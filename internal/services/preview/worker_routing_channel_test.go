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
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Release-channel placement contracts: cold-start selection must only pick
// nodes on the org's channel — a job pinned to a wrong-channel node is
// unclaimable by either pool (the claim predicate requires the channel to
// match while node affinity points elsewhere).
// See docs/design/118-canary-stable-release-channels.md.

var workerNodeChannelTestCols = []string{
	"id", "mode", "channel", "host", "status", "metadata", "started_at", "last_heartbeat_at",
}

type fakeOrgChannelLookup struct {
	channel models.ReleaseChannel
	err     error
	calls   int
}

func (f *fakeOrgChannelLookup) GetReleaseChannel(_ context.Context, _ uuid.UUID) (models.ReleaseChannel, error) {
	f.calls++
	return f.channel, f.err
}

func previewCapableMetadata(t *testing.T, baseURL string) []byte {
	t.Helper()
	raw, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewRPCAuthCheck:    true,
		PreviewInternalBaseURL: baseURL,
	})
	require.NoError(t, err, "should marshal worker metadata")
	return raw
}

func TestParseWorkerNodeWithRequirements_ChannelFilter(t *testing.T) {
	t.Parallel()

	node := models.Node{
		ID:       "worker-canary-1",
		Mode:     "worker",
		Channel:  models.ReleaseChannelCanary,
		Status:   models.NodeStatusActive,
		Metadata: previewCapableMetadata(t, "http://worker-canary-1.internal:8080"),
	}

	_, err := parseWorkerNodeWithRequirements(node, WorkerSelectionRequirements{Channel: models.ReleaseChannelStable})
	require.Error(t, err, "a canary node must be rejected for stable-channel placement")

	worker, err := parseWorkerNodeWithRequirements(node, WorkerSelectionRequirements{Channel: models.ReleaseChannelCanary})
	require.NoError(t, err, "a canary node satisfies canary-channel placement")
	require.Equal(t, "worker-canary-1", worker.ID)

	worker, err = parseWorkerNodeWithRequirements(node, WorkerSelectionRequirements{})
	require.NoError(t, err, "an empty channel requirement must stay unconstrained for single-plane deployments")
	require.Equal(t, "worker-canary-1", worker.ID)
}

func TestWorkerSelector_SelectLeastLoaded_FiltersByChannel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now().UTC()
	rows := pgxmock.NewRows(workerNodeChannelTestCols).
		AddRow("worker-stable-1", "worker", "stable", "worker-stable-1.internal", "active",
			previewCapableMetadata(t, "http://worker-stable-1.internal:8080"), now, now).
		AddRow("worker-canary-1", "worker", "canary", "worker-canary-1.internal", "active",
			previewCapableMetadata(t, "http://worker-canary-1.internal:8080"), now, now)
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(rows)
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))

	selector := NewWorkerSelector(db.NewNodeStore(mock), db.NewPreviewStore(mock))
	worker, err := selector.SelectLeastLoadedNodeWithRequirements(context.Background(),
		WorkerSelectionRequirements{Channel: models.ReleaseChannelCanary})
	require.NoError(t, err, "selection should succeed when a matching-channel node exists")
	require.Equal(t, "worker-canary-1", worker.ID,
		"only the node on the requested release channel is an eligible cold-start target")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_SelectStartNode_StampsOrgChannel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	orgID := uuid.New()
	session := &models.Session{ID: uuid.New()}
	now := time.Now().UTC()

	// No active preview instance for the session → cold start.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	rows := pgxmock.NewRows(workerNodeChannelTestCols).
		AddRow("worker-stable-1", "worker", "stable", "worker-stable-1.internal", "active",
			previewCapableMetadata(t, "http://worker-stable-1.internal:8080"), now, now).
		AddRow("worker-canary-1", "worker", "canary", "worker-canary-1.internal", "active",
			previewCapableMetadata(t, "http://worker-canary-1.internal:8080"), now, now)
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
		WillReturnRows(rows)
	mock.ExpectQuery("SELECT worker_node_id, COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"worker_node_id", "count"}))

	lookup := &fakeOrgChannelLookup{channel: models.ReleaseChannelCanary}
	selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
		OrgChannels: lookup,
	})

	worker, err := selector.SelectStartNode(context.Background(), orgID, session)
	require.NoError(t, err, "cold-start selection should succeed for the org's channel")
	require.Equal(t, "worker-canary-1", worker.ID,
		"a canary org's cold start must land on a canary worker even when a stable worker is less loaded")
	require.Equal(t, 1, lookup.calls, "the org channel should be resolved through the configured lookup")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerSelector_RequireOrgChannel(t *testing.T) {
	t.Parallel()

	t.Run("fails closed when the lookup errors", func(t *testing.T) {
		t.Parallel()
		selector := NewWorkerSelectorWithOptions(nil, nil, WorkerSelectorOptions{
			OrgChannels: &fakeOrgChannelLookup{err: errors.New("db down")},
		})
		_, err := selector.RequireOrgChannel(context.Background(), uuid.New(), WorkerSelectionRequirements{})
		require.Error(t, err, "placement must not proceed with an unknown org channel")
	})

	t.Run("no-op without a lookup or with a preset channel", func(t *testing.T) {
		t.Parallel()
		selector := NewWorkerSelector(nil, nil)
		req, err := selector.RequireOrgChannel(context.Background(), uuid.New(), WorkerSelectionRequirements{})
		require.NoError(t, err)
		require.Empty(t, req.Channel, "no lookup wired means single-plane behavior")

		lookup := &fakeOrgChannelLookup{channel: models.ReleaseChannelCanary}
		selector = NewWorkerSelectorWithOptions(nil, nil, WorkerSelectorOptions{OrgChannels: lookup})
		req, err = selector.RequireOrgChannel(context.Background(), uuid.New(),
			WorkerSelectionRequirements{Channel: models.ReleaseChannelStable})
		require.NoError(t, err)
		require.Equal(t, models.ReleaseChannelStable, req.Channel, "a preset channel must not be overridden")
		require.Zero(t, lookup.calls, "a preset channel must not trigger a lookup")
	})
}

func TestWorkerSelector_StaticEgressDiagnostics_ScopedToOrgChannel(t *testing.T) {
	t.Parallel()

	// A stable worker without static egress must not veto availability for a
	// canary org (and vice versa): only same-channel workers can claim the
	// org's session jobs.
	activeNodes := func(t *testing.T, mock pgxmock.PgxPoolIface) {
		t.Helper()
		now := time.Now().UTC()
		egressMeta, err := json.Marshal(WorkerNodeMetadata{
			StaticEgressCapable:  true,
			StaticEgressPublicIP: "203.0.113.10",
		})
		require.NoError(t, err, "should marshal worker metadata")
		rows := pgxmock.NewRows(workerNodeChannelTestCols).
			AddRow("worker-stable-1", "worker", "stable", "worker-stable-1.internal", "active",
				json.RawMessage(`{}`), now, now).
			AddRow("worker-canary-1", "worker", "canary", "worker-canary-1.internal", "active",
				egressMeta, now, now)
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE status = 'active' ORDER BY id ASC").
			WillReturnRows(rows)
	}

	t.Run("canary org sees only its capable canary worker", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()
		activeNodes(t, mock)

		selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
			OrgChannels: &fakeOrgChannelLookup{channel: models.ReleaseChannelCanary},
		})
		got, err := selector.StaticEgressWorkerDiagnostics(context.Background(), uuid.New(), "203.0.113.10")
		require.NoError(t, err, "StaticEgressWorkerDiagnostics should not error")
		require.True(t, got.Available,
			"an incapable stable worker must not veto static egress for a canary org")
		require.Empty(t, got.Mismatches, "no same-channel mismatches expected")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("stable org is not granted availability by a capable canary worker", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool")
		defer mock.Close()
		activeNodes(t, mock)

		selector := NewWorkerSelectorWithOptions(db.NewNodeStore(mock), db.NewPreviewStore(mock), WorkerSelectorOptions{
			OrgChannels: &fakeOrgChannelLookup{channel: models.ReleaseChannelStable},
		})
		got, err := selector.StaticEgressWorkerDiagnostics(context.Background(), uuid.New(), "203.0.113.10")
		require.NoError(t, err, "StaticEgressWorkerDiagnostics should not error")
		require.False(t, got.Available,
			"a capable canary worker must not grant static egress to a stable org whose own worker lacks it")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("lookup failure surfaces instead of judging the wrong fleet", func(t *testing.T) {
		t.Parallel()
		selector := NewWorkerSelectorWithOptions(nil, nil, WorkerSelectorOptions{
			OrgChannels: &fakeOrgChannelLookup{err: errors.New("db down")},
		})
		_, err := selector.StaticEgressWorkerDiagnostics(context.Background(), uuid.New(), "203.0.113.10")
		require.Error(t, err, "an unknown org channel must fail the check, not fall back to fleet-wide")
	})
}
