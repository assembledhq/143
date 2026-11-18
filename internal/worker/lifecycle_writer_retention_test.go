package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
		name      string
		enabled   bool
		counts    []int64
		failure   error
		wantRows  int64
		budgetLog bool
	}{
		{name: "unwired store remains a no-op"},
		{name: "stops after a short second batch", enabled: true, counts: []int64{1000, 7}, wantRows: 1007},
		{name: "ten batches leave remaining backlog", enabled: true, counts: []int64{1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000}, wantRows: 10000, budgetLog: true},
		{name: "empty batch", enabled: true, counts: []int64{0}},
		{name: "failed batch stays observable and retryable", enabled: true, failure: failure},
		{name: "later failure retains confirmed progress", enabled: true, counts: []int64{1000}, failure: failure, wantRows: 1000},
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
				for _, count := range tt.counts {
					mock.ExpectExec(`WITH expired AS .*LIMIT 1000.*FOR UPDATE SKIP LOCKED.*DELETE FROM lifecycle_writer_instances`).WillReturnResult(pgxmock.NewResult("DELETE", count))
				}
				if tt.failure != nil {
					mock.ExpectExec(`WITH expired AS .*LIMIT 1000.*FOR UPDATE SKIP LOCKED.*DELETE FROM lifecycle_writer_instances`).WillReturnError(tt.failure)
				}
			}
			var logs bytes.Buffer
			err = newDataRetentionCleanupHandler(stores, DataRetentionConfig{}, zerolog.New(&logs))(context.Background(), "data_retention_cleanup", json.RawMessage(`{}`))
			if tt.failure != nil {
				require.ErrorIs(t, err, tt.failure, "cleanup must retain its error for existing worker retry semantics")
				require.Contains(t, logs.String(), `"level":"warn"`, "failed passive cleanup must be logged")
				require.NotContains(t, logs.String(), "observation cleanup sweep finished", "failed cleanup must not advertise successful sweep completion")
			} else {
				require.NoError(t, err, "optional registry cleanup must preserve existing maintenance behavior")
			}
			require.Contains(t, logs.String(), `"total_deleted":`+strconv.FormatInt(tt.wantRows, 10), "maintenance must count confirmed progress even if a later batch fails or the optional store is absent")
			require.Equal(t, tt.budgetLog, bytes.Contains(logs.Bytes(), []byte("passive lifecycle writer retention sweep budget reached")), "a count-limited exit must log partial progress without claiming to drain the table")
			require.NoError(t, mock.ExpectationsWereMet(), "each scheduled job must stop after a short batch, error or ten bounded SQL calls")
		})
	}
}
