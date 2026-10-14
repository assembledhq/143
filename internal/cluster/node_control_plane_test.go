package cluster

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type heartbeatDBStub struct {
	db.DBTX
	calls atomic.Int32
}

func (s *heartbeatDBStub) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	if s.calls.Add(1) == 1 {
		<-ctx.Done()
		return pgconn.CommandTag{}, ctx.Err()
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestHeartbeatOnceBoundsFailureAndCanResume(t *testing.T) {
	t.Parallel()
	control := &heartbeatDBStub{}
	// A nil ordinary store deliberately makes any fallback fail immediately.
	n := NewNodeManager(nil, zerolog.Nop(), "test-worker", "worker", WithControlPlaneDB(control))
	n.heartbeatTimeout = 20 * time.Millisecond
	start := time.Now()
	err := n.HeartbeatOnce(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded, "a hung database attempt must end at the heartbeat budget")
	require.Less(t, time.Since(start), time.Second, "a failed heartbeat must not consume the stale-node interval")
	require.NoError(t, n.HeartbeatOnce(context.Background()), "the next heartbeat must run after a bounded failure")
	require.Equal(t, int32(2), control.calls.Load(), "heartbeats must recover on the reserved store without ordinary-pool access")
}
