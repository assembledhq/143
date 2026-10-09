package worker

import (
	"testing"
	"time"

	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/stretchr/testify/require"
)

func TestOrdinaryMergeabilityPollingGrowsFromDurableWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		elapsed, expected time.Duration
	}{
		{name: "clock skew retains initial delay", elapsed: -time.Minute, expected: 5 * time.Second},
		{name: "first poll", expected: 5 * time.Second},
		{name: "before first checkpoint", elapsed: 5*time.Second - time.Nanosecond, expected: 5 * time.Second},
		{name: "second poll", elapsed: 5 * time.Second, expected: 10 * time.Second},
		{name: "third poll", elapsed: 15 * time.Second, expected: 20 * time.Second},
		{name: "fourth poll", elapsed: 35 * time.Second, expected: 40 * time.Second},
		{name: "fifth poll", elapsed: 75 * time.Second, expected: 80 * time.Second},
		{name: "caps at two minutes", elapsed: 155 * time.Second, expected: 2 * time.Minute},
		{name: "late reclaim retains cap", elapsed: time.Hour, expected: 2 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
			retry := newPullRequestMergeabilityWait(ghservice.ErrPullRequestMergeabilityPending)
			applyGitHubRetrySchedule(retry, start, start.Add(tt.elapsed))
			require.Equal(t, tt.expected, *retry.RetryAfter, "poll delay must derive from the original durable window rather than attempts or process-local state")
			require.ErrorIs(t, retry, ghservice.ErrPullRequestMergeabilityPending, "readiness reason must survive scheduling")
			require.False(t, retry.ConsumeAttempt, "dependency wait must preserve normal attempts")
			require.False(t, retry.ResetRetryWindow, "dependency wait must preserve its durable start")
		})
	}
}

func TestOrdinaryMergeabilityPollingHasBoundedRequestVolume(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	now := start
	polls := 0
	for {
		retry := newPullRequestMergeabilityWait(ghservice.ErrPullRequestMergeabilityPending)
		applyGitHubRetrySchedule(retry, start, now)
		polls++
		exceeded, budget := retryableDurationExceeded(start, retry, now)
		require.Equal(t, 2*time.Hour, budget, "request reduction must retain the finite recovery budget")
		if exceeded {
			break
		}
		now = now.Add(*retry.RetryAfter)
	}
	require.Equal(t, 64, polls, "persistent indeterminate sync should make 64 polls over its original two-hour window instead of fixed five-second polling")
	require.LessOrEqual(t, now.Sub(start), 2*time.Hour, "final observed poll must remain inside the original dependency window")
}
