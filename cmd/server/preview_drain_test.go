package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type previewDrainCounter struct {
	counts []int
	errs   []error
	calls  int
}

func (c *previewDrainCounter) CountActivePreviewsByWorker(context.Context, string) (int, error) {
	c.calls++
	idx := c.calls - 1
	if idx < len(c.errs) && c.errs[idx] != nil {
		return 0, c.errs[idx]
	}
	if idx < len(c.counts) {
		return c.counts[idx], nil
	}
	if len(c.counts) == 0 {
		return 0, nil
	}
	return c.counts[len(c.counts)-1], nil
}

func TestWaitForActivePreviewsToDrain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		counter      *previewDrainCounter
		timeout      time.Duration
		pollInterval time.Duration
		want         bool
		wantCalls    int
	}{
		{
			name:         "returns immediately when no active previews remain",
			counter:      &previewDrainCounter{counts: []int{0}},
			timeout:      time.Minute,
			pollInterval: 10 * time.Millisecond,
			want:         true,
			wantCalls:    1,
		},
		{
			name:         "waits until active previews drain",
			counter:      &previewDrainCounter{counts: []int{2, 1, 0}},
			timeout:      time.Minute,
			pollInterval: 10 * time.Millisecond,
			want:         true,
			wantCalls:    3,
		},
		{
			name:         "keeps polling after transient count errors",
			counter:      &previewDrainCounter{errs: []error{errors.New("db unavailable")}, counts: []int{0, 0}},
			timeout:      time.Minute,
			pollInterval: 10 * time.Millisecond,
			want:         true,
			wantCalls:    2,
		},
		{
			// The poll timer must never be ready alongside the deadline; if both are,
			// select may pick the timer and poll again after the timeout has passed.
			name:         "returns false when previews outlive the timeout",
			counter:      &previewDrainCounter{counts: []int{1, 1, 1, 1}},
			timeout:      time.Millisecond,
			pollInterval: time.Hour,
			want:         false,
			wantCalls:    1,
		},
		{
			name:         "skips waiting when timeout is disabled",
			counter:      &previewDrainCounter{counts: []int{1}},
			timeout:      0,
			pollInterval: 10 * time.Millisecond,
			want:         false,
			wantCalls:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := waitForActivePreviewsToDrain(context.Background(), tt.counter, "worker-1", zerolog.Nop(), tt.timeout, tt.pollInterval)
			require.Equal(t, tt.want, got, "waitForActivePreviewsToDrain should report whether previews drained")
			require.Equal(t, tt.wantCalls, tt.counter.calls, "waitForActivePreviewsToDrain should poll the expected number of times")
		})
	}
}
