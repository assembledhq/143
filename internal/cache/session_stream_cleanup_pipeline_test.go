package cache

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type cleanupRedisHook struct {
	process  func(redis.ProcessHook) redis.ProcessHook
	pipeline func(redis.ProcessPipelineHook) redis.ProcessPipelineHook
}

func (h cleanupRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h cleanupRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	if h.process != nil {
		return h.process(next)
	}
	return next
}

func (h cleanupRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	if h.pipeline != nil {
		return h.pipeline(next)
	}
	return next
}

func TestSessionStreams_CleanupHighLatencyPipeline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		latency   time.Duration
		cancelled bool
		wantErr   error
		wantCalls int
		wantTime  time.Duration
	}{
		{name: "75ms round trip completes a full page", latency: 75 * time.Millisecond, wantCalls: 1, wantTime: 75 * time.Millisecond},
		{name: "stalled pipeline respects batch deadline", latency: time.Minute, wantErr: context.DeadlineExceeded, wantCalls: 1, wantTime: sessionCleanupBatchTimeout},
		{name: "cancelled batch sends no deletes", cancelled: true, wantErr: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				// Intercept the real go-redis command/pipeline boundary, charging
				// one simulated network round trip per send without real sockets.
				rdb := redis.NewClient(&redis.Options{Addr: "unused:6379"})
				defer func() { require.NoError(t, rdb.Close(), "close the isolated Redis client") }()
				var calls int
				var sent [][]any
				send := func(ctx context.Context, cmds []redis.Cmder) error {
					calls++
					timer := time.NewTimer(tt.latency)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
					}
					for _, cmd := range cmds {
						sent = append(sent, cmd.Args())
						cmd.(*redis.IntCmd).SetVal(0) // Already expired keys are still successful cleanup.
					}
					return nil
				}
				rdb.AddHook(cleanupRedisHook{
					process: func(_ redis.ProcessHook) redis.ProcessHook {
						return func(ctx context.Context, cmd redis.Cmder) error { return send(ctx, []redis.Cmder{cmd}) }
					},
					pipeline: func(_ redis.ProcessPipelineHook) redis.ProcessPipelineHook { return send },
				})
				client := &Client{rdb: rdb, breaker: NewCircuitBreaker(zerolog.Nop()), logger: zerolog.Nop()}
				streams := NewSessionStreams(client, zerolog.Nop(), nil)
				start := time.Now()
				page := cleanupPage(start.Add(-time.Hour), 0, sessionCleanupBatchSize)
				ctx, cancel := context.WithTimeout(context.Background(), sessionCleanupBatchTimeout)
				defer cancel()
				if tt.cancelled {
					cancel()
				}
				count, next, err := streams.runCleanupBatch(ctx, cleanupTestLister{sessions: page}, start, nil)
				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr, "cancellation must remain bounded and preserve retry semantics")
					require.Zero(t, count, "failed pipelines must not claim confirmed page completion")
					require.Nil(t, next, "failed pipelines must not advance the cursor")
				} else {
					require.NoError(t, err, "500 sessions must fit the deadline despite 75ms Redis latency")
					require.Equal(t, sessionCleanupBatchSize, count, "a full page must finish even when keys already expired")
					require.Equal(t, &page[len(page)-1], next, "successful pipelining must advance to the last session")
					expected := make([][]any, len(page))
					for i, session := range page {
						prefix := "143:stream:{ses:" + session.ID.String() + "}:"
						expected[i] = []any{"del", prefix + "logs", prefix + "status", prefix + "events"}
					}
					require.Equal(t, expected, sent, "each pipelined DEL must contain only one session hash slot")
				}
				require.Equal(t, tt.wantCalls, calls, "cleanup must avoid one round trip per session")
				require.Equal(t, tt.wantTime, time.Since(start), "network latency must apply per pipeline, within the original deadline")
			})
		})
	}
}

func TestSessionStreams_CleanupPipelinePartialFailureRetry(t *testing.T) {
	t.Parallel()
	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	before := time.Now()
	page := cleanupPage(before.Add(-time.Hour), 0, 2)
	for _, session := range page {
		for _, key := range []string{logStreamKey(session.ID), statusStreamKey(session.ID), eventStreamKey(session.ID)} {
			require.NoError(t, mr.Set(key, "saved"), "seed all three stream keys for each session")
		}
	}
	failure := errors.New("pipeline interrupted after first reply")
	injected := false
	client.raw().AddHook(cleanupRedisHook{pipeline: func(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
		return func(ctx context.Context, cmds []redis.Cmder) error {
			if !injected {
				injected = true
				if err := next(ctx, cmds[:1]); err != nil {
					return err
				}
				for _, cmd := range cmds[1:] {
					cmd.SetErr(failure)
				}
				return failure
			}
			return next(ctx, cmds)
		}
	}})
	count, cursor, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{sessions: page}, before, nil)
	require.ErrorIs(t, err, failure, "an incomplete pipeline must be reported for backoff")
	require.Zero(t, count, "an incomplete pipeline must not claim the page completed")
	require.Nil(t, cursor, "partial deletion must not advance past unconfirmed keys")
	require.Equal(t, []string{eventStreamKey(page[1].ID), logStreamKey(page[1].ID), statusStreamKey(page[1].ID)}, mr.Keys(), "only the first session should be deleted before the simulated failure")
	count, cursor, err = streams.runCleanupBatch(context.Background(), cleanupTestLister{sessions: page}, before, nil)
	require.NoError(t, err, "retrying the entire page must tolerate already-deleted keys")
	require.Equal(t, len(page), count, "retry must finish all sessions")
	require.Equal(t, &page[1], cursor, "only a successful retry may advance the cursor")
	require.Empty(t, mr.Keys(), "retry must remove the remaining stream keys")
}
