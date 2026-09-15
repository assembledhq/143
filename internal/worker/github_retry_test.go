package worker

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
)

func TestGitHubRateLimitRetryAfter(t *testing.T) {
	t.Parallel()

	zero := time.Duration(0)
	short := 17 * time.Second
	long := 117 * time.Second
	tests := []struct {
		name     string
		upstream *time.Duration
		retryKey string
		expected time.Duration
	}{
		{name: "missing hint uses floor and jitter", retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "zero hint uses floor and jitter", upstream: &zero, retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "short hint uses floor and jitter", upstream: &short, retryKey: "00000000-0000-0000-0000-000000000143", expected: 82 * time.Second},
		{name: "long hint preserves upstream wait and adds jitter", upstream: &long, retryKey: "00000000-0000-0000-0000-000000000143", expected: 139 * time.Second},
		{name: "different retry key receives different stable jitter", retryKey: "11111111-2222-3333-4444-555555555555", expected: 78 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := githubRateLimitRetryAfter(tt.upstream, tt.retryKey)
			require.NotNil(t, actual, "rate-limit policy should always return an explicit delay")
			require.Equal(t, tt.expected, *actual, "rate-limit policy should apply the expected floor and deterministic jitter")
		})
	}
}

func TestGitHubControllerDeadlineIsScheduledExactly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	retryAt := now.Add(17 * time.Second)
	retryable := githubRetryableError(&ratelimit.Deferral{
		Kind: ratelimit.KindSecondary, InstallationID: 42, RetryAt: retryAt, Generation: 3,
	}, "must-not-add-jitter")
	require.NotNil(t, retryable, "controller deferral should enter the GitHub recovery policy")
	require.Equal(t, GitHubRetryPolicyExact, retryable.GitHubRetryPolicy, "controller deferral should select exact scheduling")
	require.False(t, retryable.ConsumeAttempt, "controller deferral should preserve attempts")
	require.Nil(t, retryable.RetryAfter, "exact delay should be materialized from the worker scheduling clock")

	applyGitHubRetrySchedule(retryable, now.Add(-time.Hour), now)
	require.NotNil(t, retryable.RetryAfter, "worker should materialize the exact controller delay")
	require.Equal(t, 17*time.Second, *retryable.RetryAfter, "worker should use the controller deadline without a floor or jitter")
}

func TestGitHubNoHintRetryScheduleUsesDurableIncreasingSlots(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	retryKey := "00000000-0000-0000-0000-000000000143"
	jitter := githubRateLimitJitter(retryKey)
	tests := []struct {
		name      string
		failureAt time.Time
		expected  time.Time
	}{
		{name: "first throttle uses one minute slot", failureAt: startedAt, expected: startedAt.Add(time.Minute + jitter)},
		{name: "restart after first slot uses three minute slot", failureAt: startedAt.Add(time.Minute), expected: startedAt.Add(3*time.Minute + jitter)},
		{name: "restart after second slot uses seven minute slot", failureAt: startedAt.Add(3 * time.Minute), expected: startedAt.Add(7*time.Minute + jitter)},
		{name: "steady state advances five minutes", failureAt: startedAt.Add(7 * time.Minute), expected: startedAt.Add(12*time.Minute + jitter)},
		{name: "minimum wait survives timestamp precision", failureAt: startedAt.Add(11*time.Minute + 59*time.Second), expected: startedAt.Add(12*time.Minute + 59*time.Second + jitter)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := githubNoHintRetryAt(startedAt, tt.failureAt, retryKey)
			require.Equal(t, tt.expected, actual, "no-hint schedule should remain anchored across worker restarts")
			require.LessOrEqual(t, actual.Sub(tt.failureAt), 5*time.Minute+29*time.Second, "local scheduling delay should remain within the recovery-latency bound")
		})

	}

	formerSchedule := make([]time.Duration, 0)
	for elapsed := time.Minute + jitter; elapsed <= 12*time.Minute; elapsed += time.Minute + jitter {
		formerSchedule = append(formerSchedule, elapsed)
	}
	newSchedule := []time.Duration{time.Minute + jitter, 3*time.Minute + jitter, 7*time.Minute + jitter}
	require.Equal(t, []time.Duration{82 * time.Second, 164 * time.Second, 246 * time.Second, 328 * time.Second, 410 * time.Second, 492 * time.Second, 574 * time.Second, 656 * time.Second}, formerSchedule, "former one-minute-plus-jitter loop should have the exact twelve-minute request schedule")
	require.Equal(t, []time.Duration{82 * time.Second, 202 * time.Second, 442 * time.Second}, newSchedule, "durable slots should have the exact reduced twelve-minute request schedule")
	require.Less(t, len(newSchedule), len(formerSchedule), "increasing slots should issue fewer requests during a sustained no-hint throttle")
}

func TestGitHubNoHintRetryScheduleMeetsMaximumRecoveryLatencyAtEveryRung(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	retryKey := ""
	for candidate := 0; candidate < 10_000; candidate++ {
		key := fmt.Sprintf("max-jitter-%d", candidate)
		if githubRateLimitJitter(key) == 29*time.Second {
			retryKey = key
			break
		}
	}
	require.NotEmpty(t, retryKey, "test should find a deterministic key with maximum jitter")

	failureAt := startedAt
	for rung := 0; failureAt.Before(startedAt.Add(githubRateLimitMaxRetryDuration)); rung++ {
		retryAt := githubNoHintRetryAt(startedAt, failureAt, retryKey)
		require.LessOrEqual(t, retryAt.Sub(failureAt), 5*time.Minute+29*time.Second, "every retry rung should preserve the maximum local recovery-latency bound")
		if retryAt.After(startedAt.Add(githubRateLimitMaxRetryDuration)) {
			break
		}
		failureAt = retryAt.Add(time.Nanosecond)
	}
}
