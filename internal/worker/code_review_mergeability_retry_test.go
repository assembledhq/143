package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSyncCodeReviewMergeabilityWaitUsesBoundedRetryWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		persisted bool
		elapsed   time.Duration
		wrapped   bool
		expired   bool
	}{
		{name: "first wait starts a window even for an old controller"},
		{name: "wrapped pending error starts the same window", wrapped: true},
		{name: "ordinary wait preserves its original deadline", persisted: true, elapsed: time.Hour},
		{name: "retry exactly at deadline is allowed", persisted: true, elapsed: 2*time.Hour - 5*time.Second},
		{name: "retry beyond deadline is rejected", persisted: true, elapsed: 2*time.Hour - 4*time.Second, expired: true},
		{name: "persistent GitHub wait expires", persisted: true, elapsed: 3 * time.Hour, expired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstreamErr := ghservice.ErrPullRequestMergeabilityPending
			if tt.wrapped {
				upstreamErr = fmt.Errorf("refresh pull request: %w", upstreamErr)
			}
			services := &Services{PR: &stubPRService{
				syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error {
					return upstreamErr
				},
			}}
			err := syncCodeReviewPullRequestState(context.Background(), services, zerolog.Nop(), runCodeReviewPayload{
				OrgID: uuid.New(), PullRequestID: uuid.New(), SessionID: uuid.New(),
			})
			var retry *RetryableError
			require.ErrorAs(t, err, &retry, "pending mergeability should remain a retryable dependency wait")
			require.ErrorIs(t, err, ghservice.ErrPullRequestMergeabilityPending, "retry should preserve the GitHub wait reason")
			require.False(t, retry.ConsumeAttempt, "dependency waits should not exhaust the ordinary attempt count")
			require.False(t, retry.BypassMaxRetryDuration, "an external wait must not bypass terminal recovery")
			require.NotNil(t, retry.MaxRetryDuration, "mergeability waits should use a durable bounded recovery window")
			require.Equal(t, 2*time.Hour, *retry.MaxRetryDuration, "mergeability waits should use the existing GitHub recovery budget")
			require.NotNil(t, retry.RetryAfter, "mergeability retries should retain their polling delay")
			require.Equal(t, 5*time.Second, *retry.RetryAfter, "short GitHub computations should retain prompt retries")

			now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
			startedAt := now.Add(-tt.elapsed)
			lockToken := uuid.New()
			job := &models.Job{ID: uuid.New(), LockToken: &lockToken, CreatedAt: now.Add(-24 * time.Hour)}
			store := &retryWindowLeaseStoreStub{startedAt: startedAt, ok: true}
			if tt.persisted {
				job.RetryWindowStartedAt = &startedAt
			}
			actualStart, owned, startErr := ensureRetryWindowStartedAt(context.Background(), store, job, retry, now)
			require.NoError(t, startErr, "controller should establish its durable wait deadline")
			require.True(t, owned, "retry deadline must remain protected by job ownership")
			require.Equal(t, startedAt, actualStart, "wait should use the persisted start instead of controller creation time")
			expired, window := retryableDurationExceeded(actualStart, retry, now)
			require.Equal(t, tt.expired, expired, "worker should enforce the mergeability wait deadline including the next delay")
			require.Equal(t, 2*time.Hour, window, "worker should apply the GitHub recovery window")

			// Reclaiming the job must not grant a new window on every poll.
			reclaimed := &models.Job{ID: job.ID, LockToken: &lockToken, CreatedAt: job.CreatedAt, RetryWindowStartedAt: job.RetryWindowStartedAt}
			nextStart, owned, startErr := ensureRetryWindowStartedAt(context.Background(), store, reclaimed, retry, now.Add(time.Minute))
			require.NoError(t, startErr, "reclaimed retry should reuse the durable deadline")
			require.True(t, owned, "reclaimed retry should retain ownership")
			require.Equal(t, startedAt, nextStart, "repeated polls must not reset the retry window")
			expectedWrites := 1
			if tt.persisted {
				expectedWrites = 0
			}
			require.Equal(t, expectedWrites, store.calls, "only the first bounded wait should persist its start")
		})
	}
}
