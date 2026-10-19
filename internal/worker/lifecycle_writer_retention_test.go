package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLifecycleWriterRetentionBatch(t *testing.T) {
	t.Parallel()
	failure := errors.New("synthetic cleanup failure")
	tests := []struct {
		name     string
		enabled  bool
		failure  error
		wantRows int64
	}{
		{name: "unwired store remains a no-op"},
		{name: "one bounded batch even with backlog", enabled: true, wantRows: 1000},
		{name: "empty batch", enabled: true},
		{name: "failed batch stays observable and retryable", enabled: true, failure: failure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "construct an isolated global retention fixture")
			defer mock.Close()
			stores := &Stores{}
			if tt.enabled {
				stores.LifecycleWriters = db.NewLifecycleWriterInstanceStore(mock)
				query := mock.ExpectExec(`WITH expired AS .*LIMIT 1000.*FOR UPDATE SKIP LOCKED.*DELETE FROM lifecycle_writer_instances`)
				if tt.failure != nil {
					query.WillReturnError(tt.failure)
				} else {
					query.WillReturnResult(pgxmock.NewResult("DELETE", tt.wantRows))
				}
			}
			var logs bytes.Buffer
			err = newDataRetentionCleanupHandler(stores, DataRetentionConfig{}, zerolog.New(&logs))(context.Background(), "data_retention_cleanup", json.RawMessage(`{}`))
			if tt.failure != nil {
				require.ErrorIs(t, err, tt.failure, "cleanup must retain its error for existing worker retry semantics")
				require.Contains(t, logs.String(), `"level":"warn"`, "failed passive cleanup must be logged")
				require.NotContains(t, logs.String(), "observation cleanup batch complete", "failed cleanup must not advertise a successful batch")
			} else {
				require.NoError(t, err, "optional registry cleanup must preserve existing maintenance behavior")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "each scheduled job may issue at most one bounded global batch")
		})
	}
}
