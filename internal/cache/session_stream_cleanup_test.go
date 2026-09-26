package cache

import (
	"context"
	"encoding/binary"
	"testing"
	"testing/synctest"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type cleanupListerFunc func(context.Context, time.Time, *models.SessionStreamCleanupCursor, int) ([]models.SessionStreamCleanupCursor, error)

func (f cleanupListerFunc) ListTerminalEndedBefore(ctx context.Context, before time.Time, after *models.SessionStreamCleanupCursor, limit int) ([]models.SessionStreamCleanupCursor, error) {
	return f(ctx, before, after, limit)
}

func cleanupPage(completedAt time.Time, start, count int) []models.SessionStreamCleanupCursor {
	page := make([]models.SessionStreamCleanupCursor, count)
	for i := range page {
		var id uuid.UUID
		binary.BigEndian.PutUint64(id[8:], uint64(start+i+1))
		page[i] = models.SessionStreamCleanupCursor{ID: id, CompletedAt: completedAt}
	}
	return page
}

func TestSessionStreams_CleanupLoopPaginationAndBackoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fullPages   int
		queryError  bool
		repeatPage  bool
		queryStalls bool
	}{
		{name: "more than 500 rows advance then return to idle cadence", fullPages: 1},
		{name: "batch cap pauses without losing progress", fullPages: sessionCleanupMaxBatches + 1},
		{name: "query failure backs off and retries the same page", fullPages: 1, queryError: true},
		{name: "repeated page backs off without resetting the cursor", fullPages: 1, repeatPage: true},
		{name: "query timeout bounds a wake", fullPages: 1, queryStalls: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				// Redis deletion is covered with miniredis separately. This test
				// exercises the real loop/timers with virtual time and no network.
				streams := &SessionStreams{logger: zerolog.Nop()}
				start := time.Now()
				completed := start.Add(-2 * time.Hour)
				var calls int
				var lastCall time.Time
				var firstCutoff time.Time
				var previousLast *models.SessionStreamCleanupCursor
				injectedFailure := false
				finishedSweep := false
				fullPages := 0
				expectDelay := sessionCleanupInterval
				lister := cleanupListerFunc(func(queryCtx context.Context, before time.Time, after *models.SessionStreamCleanupCursor, limit int) ([]models.SessionStreamCleanupCursor, error) {
					calls++
					require.Equal(t, sessionCleanupBatchSize, limit, "every page must remain bounded")
					deadline, ok := queryCtx.Deadline()
					require.True(t, ok, "each cleanup page must have a deadline")
					require.Equal(t, sessionCleanupBatchTimeout, deadline.Sub(time.Now()), "a stalled query must not hold cleanup indefinitely")
					if calls == 1 {
						require.Nil(t, after, "first sweep must start before any cursor")
						require.Equal(t, sessionCleanupInterval, time.Since(start), "startup must retain the idle delay")
						firstCutoff = before
					} else {
						require.Equal(t, expectDelay, time.Since(lastCall), "cleanup must pace pages and back off after caps or errors")
					}
					lastCall = time.Now()
					if finishedSweep {
						require.Nil(t, after, "a completed sweep should restart from the beginning")
						require.True(t, before.After(firstCutoff), "only the next sweep should refresh eligibility")
						cancel()
						return nil, context.Canceled
					}
					require.Equal(t, firstCutoff, before, "all pages including later wakes must share the same cutoff")
					require.Equal(t, previousLast, after, "the next query must resume after the last successful page")
					if fullPages == 1 && !injectedFailure && (tt.queryError || tt.repeatPage || tt.queryStalls) {
						injectedFailure = true
						expectDelay = sessionCleanupInterval
						if tt.repeatPage {
							return cleanupPage(completed, 0, sessionCleanupBatchSize), nil
						}
						if tt.queryStalls {
							<-queryCtx.Done()
							expectDelay += sessionCleanupBatchTimeout
						}
						return nil, context.DeadlineExceeded
					}
					if fullPages < tt.fullPages {
						page := cleanupPage(completed, fullPages*sessionCleanupBatchSize, sessionCleanupBatchSize)
						last := page[len(page)-1]
						previousLast = &last
						fullPages++
						expectDelay = sessionCleanupBatchInterval
						if fullPages%sessionCleanupMaxBatches == 0 {
							expectDelay = sessionCleanupInterval
						}
						return page, nil
					}
					finishedSweep = true
					expectDelay = sessionCleanupInterval
					return cleanupPage(completed, fullPages*sessionCleanupBatchSize, 2), nil
				})
				streams.cleanupLoop(ctx, lister)
				expectedCalls := tt.fullPages + 2 // final short page, then the next sweep
				if injectedFailure {
					expectedCalls++
				}
				require.Equal(t, expectedCalls, calls, "cleanup must finish the sweep without repeating or starving pages")
			})
		})
	}
}

func TestSessionStreams_CleanupLoopCancellation(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		streams := &SessionStreams{logger: zerolog.Nop()}
		calls := 0
		lister := cleanupListerFunc(func(context.Context, time.Time, *models.SessionStreamCleanupCursor, int) ([]models.SessionStreamCleanupCursor, error) {
			calls++
			return nil, nil
		})
		done := make(chan struct{})
		go func() {
			streams.cleanupLoop(ctx, lister)
			close(done)
		}()
		synctest.Wait()
		cancel()
		<-done
		require.Equal(t, 0, calls, "canceling the idle wait must stop cleanup without querying")
	})
}

func TestSessionStreams_CleanupRejectsInvalidPageBeforeDeletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind string
	}{
		{name: "duplicate cursor", kind: "duplicate"},
		{name: "timestamp moves backwards", kind: "backwards"},
		{name: "unordered IDs at the same time", kind: "unordered"},
		{name: "session is too recent", kind: "recent"},
		{name: "missing completion time", kind: "missing"},
		{name: "missing session ID", kind: "nil_id"},
		{name: "oversized page", kind: "oversized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, mr := testRedisClient(t)
			streams := NewSessionStreams(client, zerolog.Nop(), nil)
			before := time.Now()
			page := cleanupPage(before.Add(-time.Hour), 0, 2)
			var after *models.SessionStreamCleanupCursor
			switch tt.kind {
			case "duplicate":
				after = &page[0]
			case "backwards":
				page[1].CompletedAt = page[0].CompletedAt.Add(-time.Second)
			case "unordered":
				page[0], page[1] = page[1], page[0]
			case "recent":
				page[1].CompletedAt = before
			case "missing":
				page[1].CompletedAt = time.Time{}
			case "nil_id":
				page[1].ID = uuid.Nil
			case "oversized":
				page = cleanupPage(before.Add(-time.Hour), 0, sessionCleanupBatchSize+1)
			}
			key := logStreamKey(page[0].ID)
			_, err := mr.XAdd(key, "1-0", []string{"json", "{}"})
			require.NoError(t, err, "seed a key that must survive an invalid page")
			count, next, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{sessions: page}, before, after)
			require.Error(t, err, "invalid pagination must stop the batch")
			require.Zero(t, count, "invalid pages must be rejected before any deletion")
			require.Nil(t, next, "invalid pages must not publish a new cursor")
			require.True(t, mr.Exists(key), "an invalid page must not delete Redis keys")
		})
	}
}

func TestSessionStreams_CleanupRedisFailureCanRetry(t *testing.T) {
	t.Parallel()
	client, mr := testRedisClient(t)
	streams := NewSessionStreams(client, zerolog.Nop(), nil)
	before := time.Now()
	page := cleanupPage(before.Add(-time.Hour), 0, 1)
	key := logStreamKey(page[0].ID)
	_, err := mr.XAdd(key, "1-0", []string{"json", "{}"})
	require.NoError(t, err, "seed a stream for retry")
	mr.SetError("ERR unavailable")
	count, next, err := streams.runCleanupBatch(context.Background(), cleanupTestLister{sessions: page}, before, nil)
	require.Error(t, err, "Redis failures must propagate for backoff")
	require.Zero(t, count, "failed deletion must not count as successful cleanup")
	require.Nil(t, next, "Redis failure must not advance pagination")
	require.True(t, mr.Exists(key), "failed deletion must leave the stream for retry")
	mr.SetError("")
	count, next, err = streams.runCleanupBatch(context.Background(), cleanupTestLister{sessions: page}, before, nil)
	require.NoError(t, err, "the same page should be retryable after Redis recovers")
	require.Equal(t, 1, count, "retry should complete the failed page")
	require.Equal(t, &page[0], next, "only successful retry should advance pagination")
	require.False(t, mr.Exists(key), "successful retry should delete the stream")
}
