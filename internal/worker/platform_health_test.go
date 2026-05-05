package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
)

type stubQueueHealthStore struct {
	samples []db.JobQueueHealthSample
	err     error
}

func (s *stubQueueHealthStore) QueueHealthSamples(ctx context.Context) ([]db.JobQueueHealthSample, error) {
	return s.samples, s.err
}

func TestRunQueueHealthSamplerEmitsStructuredSamples(t *testing.T) {
	t.Parallel()

	store := &stubQueueHealthStore{samples: []db.JobQueueHealthSample{
		{
			Queue:                    "agent",
			JobType:                  "run_agent",
			PendingRunnable:          3,
			PendingDeferred:          2,
			Running:                  1,
			DeadLetter:               0,
			OldestRunnableAgeSeconds: 42,
		},
	}}
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		RunQueueHealthSampler(ctx, store, zerolog.New(logs), time.Hour)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return bytes.Contains(logs.Bytes(), []byte("platform health: job queue sample"))
	}, time.Second, 10*time.Millisecond, "queue sampler should emit an initial health sample")
	cancel()
	<-done

	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event), "queue health sample should be valid JSON")
	require.Equal(t, "platform health: job queue sample", event["message"], "queue sampler should use the canonical log message")
	require.Equal(t, "agent", event["queue"], "queue sampler should include queue")
	require.Equal(t, "run_agent", event["job_type"], "queue sampler should include job type")
	require.Equal(t, float64(3), event["pending_runnable"], "queue sampler should include runnable pending count")
	require.Equal(t, float64(2), event["pending_deferred"], "queue sampler should include deferred pending count")
	require.Equal(t, float64(1), event["running"], "queue sampler should include running count")
	require.Equal(t, float64(0), event["dead_letter"], "queue sampler should include dead-letter count")
	require.Equal(t, float64(42), event["oldest_runnable_age_seconds"], "queue sampler should include oldest runnable age")
}

// syncBuffer is a thread-safe bytes.Buffer wrapper for capturing zerolog output
// when a writer goroutine and an Eventually reader goroutine touch the same buffer.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buf.Bytes()...)
}
