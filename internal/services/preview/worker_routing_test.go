package preview

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
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
