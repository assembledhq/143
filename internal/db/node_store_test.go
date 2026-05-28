package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var nodeStoreTestCols = []string{
	"id", "mode", "host", "status", "metadata", "started_at", "last_heartbeat_at",
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
					AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now),
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
					AddRow("worker-1", "worker", "worker-1.internal", "active", firstMeta, now, now).
					AddRow("worker-2", "api", "api-1.internal", "active", secondMeta, now, now),
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
					AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, "not-a-time", now),
			)

		store := NewNodeStore(mock)
		_, err = store.ListActive(context.Background())
		require.Error(t, err, "ListActive should surface scan failures")
		require.Contains(t, err.Error(), "scan active nodes", "ListActive should wrap scan failures with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
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
