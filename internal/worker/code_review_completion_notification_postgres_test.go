package worker

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRecoveredCodeReviewCompletionPublishesCommittedSnapshotPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for committed completion notification proof")
	}
	pool := fullRecoveryPostgresPool(t, context.Background())
	tests := []struct {
		name          string
		recoverFailed bool
		origin        models.SessionOrigin
		wantPublished bool
	}{
		{name: "successful recovery publishes committed completion", recoverFailed: true, origin: models.SessionOriginCodeReview, wantPublished: true},
		{name: "stale reconciliation does not publish an unrelated completion", origin: models.SessionOriginCodeReview},
		{name: "other session origins remain untouched", recoverFailed: true, origin: models.SessionOriginManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, _ := seedConfirmedDrainReview(t, pool, "completed")
			ctx := f.ctx()
			_, err := pool.Exec(ctx, `UPDATE sessions SET origin=$3,completed_at=now()-interval '10 minutes' WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.SessionID, tt.origin)
			require.NoError(t, err, "seed an already committed parent completion")
			before, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read complete committed parent snapshot")
			mr := miniredis.RunT(t)
			client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
			require.NotNil(t, client, "initialize isolated session notification cache")
			defer client.Close()
			f.stores.Sessions.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))
			reconcileCodeReviewSessionCompletion(ctx, f.stores, zerolog.Nop(), f.job, tt.recoverFailed)
			after, err := f.stores.Sessions.GetByID(ctx, f.job.OrgID, f.job.SessionID)
			require.NoError(t, err, "read parent after notification reconciliation")
			require.Equal(t, before, after, "notification must not rewrite the committed status or timestamps")
			key := "143:stream:{ses:" + f.job.SessionID.String() + "}:status"
			require.Equal(t, tt.wantPublished, mr.Exists(key), "publish only the successful code review recovery snapshot")
			if tt.wantPublished {
				entries, err := mr.Stream(key)
				require.NoError(t, err, "read committed completion status event")
				require.Len(t, entries, 1, "reconciliation emits one committed snapshot")
				var published models.Session
				require.NoError(t, json.Unmarshal([]byte(entries[0].Values[1]), &published), "decode published complete session")
				encoded, err := json.Marshal(before)
				require.NoError(t, err, "encode expected committed snapshot")
				var expected models.Session
				require.NoError(t, json.Unmarshal(encoded, &expected), "normalize committed snapshot for JSON transport")
				require.Equal(t, expected, published, "subscribers receive the exact committed session snapshot")
			}
		})
	}
}
