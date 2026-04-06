package preview

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewCleanupWorker_Defaults(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger: zerolog.Nop(),
	})
	require.Equal(t, 1*time.Minute, w.interval)
	require.Equal(t, DefaultIdleTimeout, w.idleTimeout)
}

func TestNewCleanupWorker_CustomConfig(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger:      zerolog.Nop(),
		Interval:    30 * time.Second,
		IdleTimeout: 5 * time.Minute,
	})
	require.Equal(t, 30*time.Second, w.interval)
	require.Equal(t, 5*time.Minute, w.idleTimeout)
}

func TestCleanupWorker_StartStop(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger:   zerolog.Nop(),
		Interval: 100 * time.Millisecond, // fast for testing
	})

	w.Start()
	// Give it a moment to start.
	time.Sleep(50 * time.Millisecond)
	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}
