package db

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestNodeStoreLifecycleWriterVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
		args        []any
		read        func(*NodeStore) ([]models.Node, error)
	}{
		{name: "get by id", query: `SELECT .+lifecycle_writer_version FROM nodes WHERE id = @id`, args: []any{pgx.NamedArgs{"id": "node-1"}}, read: func(s *NodeStore) ([]models.Node, error) {
			node, err := s.GetByID(context.Background(), "node-1")
			if err != nil {
				return nil, err
			}
			return []models.Node{*node}, nil
		}},
		{name: "latest by host", query: `SELECT .+lifecycle_writer_version FROM nodes WHERE host = @host`, args: []any{pgx.NamedArgs{"host": "worker.invalid"}}, read: func(s *NodeStore) ([]models.Node, error) {
			node, err := s.GetLatestByHost(context.Background(), "worker.invalid")
			if err != nil {
				return nil, err
			}
			return []models.Node{*node}, nil
		}},
		{name: "active", query: `SELECT .+lifecycle_writer_version FROM nodes WHERE status = 'active'`, read: func(s *NodeStore) ([]models.Node, error) {
			return s.ListActive(context.Background())
		}},
		{name: "preview probes", query: `SELECT .+lifecycle_writer_version FROM nodes WHERE status IN`, read: func(s *NodeStore) ([]models.Node, error) {
			return s.ListPreviewRPCProbeNodes(context.Background())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			versions := []struct {
				name  string
				value int
			}{{name: "passive", value: 0}, {name: "future metadata", value: 7}}
			for _, version := range versions {
				t.Run(version.name, func(t *testing.T) {
					t.Parallel()
					mock, err := pgxmock.NewPool()
					require.NoError(t, err, "create an isolated node read store")
					defer mock.Close()
					stamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
					metadata := json.RawMessage(`{"synthetic":true}`)
					mock.ExpectQuery(tt.query).WithArgs(tt.args...).WillReturnRows(pgxmock.NewRows(nodeStoreTestCols).
						AddRow("node-1", "worker", "worker.invalid", "active", "none", metadata, stamp, stamp, nil, nil, "", "", version.value))
					actual, err := tt.read(NewNodeStore(mock))
					require.NoError(t, err, "all existing node projections should decode passive or future metadata")
					expected := []models.Node{{ID: "node-1", Mode: models.NodeModeWorker, Host: "worker.invalid", Status: models.NodeStatusActive,
						DrainIntent: models.DrainIntentNone, Metadata: metadata, StartedAt: stamp, LastHeartbeatAt: stamp, LifecycleWriterVersion: version.value}}
					require.Equal(t, expected, actual, "the full node row must survive without substituting zero or rejecting a future version")
					raw, err := json.Marshal(actual[0])
					require.NoError(t, err, "serialize the read-only node model")
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(raw, &fields), "inspect the serialized node metadata")
					require.Equal(t, json.RawMessage(strconv.Itoa(version.value)), fields["lifecycle_writer_version"], "zero must remain explicit in the read model rather than disappear through omitempty")
					require.NoError(t, mock.ExpectationsWereMet(), "each projection must select the actual version column")
				})
			}
		})
	}
}
