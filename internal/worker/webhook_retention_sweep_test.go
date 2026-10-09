package worker

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDataRetentionHandler_WebhookBoundedSweep(t *testing.T) {
	t.Parallel()
	dbErr := errors.New("webhook cleanup connection failed")
	tests := []struct {
		name          string
		counts        []int64
		failAfter     bool
		expectedCount int64
		budgetLog     bool
	}{
		{name: "resumes independently committed batches", counts: []int64{10000, 10000, 7}, expectedCount: 20007},
		{name: "leaves backlog after ten batches", counts: []int64{10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000}, expectedCount: 100000, budgetLog: true},
		{name: "later database error retains confirmed progress", counts: []int64{10000}, expectedCount: 10000, failAfter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated webhook retention database mock")
			defer mock.Close()
			for _, count := range tt.counts {
				mock.ExpectQuery("SELECT delete_expired_webhook_deliveries").WithArgs(30).
					WillReturnRows(pgxmock.NewRows([]string{"deleted"}).AddRow(count))
			}
			if tt.failAfter {
				mock.ExpectQuery("SELECT delete_expired_webhook_deliveries").WithArgs(30).WillReturnError(dbErr)
			}
			var logs bytes.Buffer
			stores := &Stores{Webhooks: db.NewWebhookDeliveryStore(mock)}
			err = newDataRetentionCleanupHandler(stores, DataRetentionConfig{WebhookDays: 30}, zerolog.New(&logs))(context.Background(), "data_retention_cleanup", nil)
			if tt.failAfter {
				require.ErrorIs(t, err, dbErr, "later batch failures must reach the job retry mechanism")
			} else {
				require.NoError(t, err, "exhaustion and bounded partial progress should both complete normally")
			}
			require.Contains(t, logs.String(), `"deleted":`+strconv.FormatInt(tt.expectedCount, 10), "handler should report confirmed progress even if a later batch fails")
			require.Equal(t, tt.budgetLog, bytes.Contains(logs.Bytes(), []byte("webhook delivery retention sweep budget reached")), "batch-budget exit should be explicit and domain-specific")
			require.NoError(t, mock.ExpectationsWereMet(), "handler must execute only the expected bounded batches")
		})
	}
}
