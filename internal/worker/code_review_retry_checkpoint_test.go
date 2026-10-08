package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// The persisted job is copied on claim, just as a database reclaim would return
// a new row. Only successful fenced writes may change its retry window.
type checkpointRetryStore struct {
	terminalLeaseStoreStub
	job       models.Job
	loseLease bool
}

func (s *checkpointRetryStore) ClaimNextRunnable(_ context.Context, _, _ string, token uuid.UUID, _ time.Duration) (*models.Job, error) {
	s.job.LockToken = &token
	s.job.Status = models.JobStatusRunning
	s.job.Attempts++
	claimed := s.job
	return &claimed, nil
}

func (s *checkpointRetryStore) EnsureRetryWindowStartedAtWithLease(_ context.Context, _, token uuid.UUID, start time.Time) (time.Time, bool, error) {
	if s.job.LockToken == nil || *s.job.LockToken != token {
		return time.Time{}, false, nil
	}
	if s.job.RetryWindowStartedAt == nil {
		s.job.RetryWindowStartedAt = &start
	}
	return *s.job.RetryWindowStartedAt, true, nil
}

func (s *checkpointRetryStore) RetryWithoutConsumingAttemptWithLease(_ context.Context, _, token uuid.UUID, _ string, _ time.Time) (bool, error) {
	if s.loseLease || s.job.LockToken == nil || *s.job.LockToken != token {
		return false, nil
	}
	s.job.Attempts--
	s.job.Status = models.JobStatusPending
	s.job.LockToken = nil
	return true, nil
}

func (s *checkpointRetryStore) RetryWithoutConsumingAttemptWithLeaseAndResetWindow(ctx context.Context, id, token uuid.UUID, message string, runAt time.Time) (bool, error) {
	ok, err := s.RetryWithoutConsumingAttemptWithLease(ctx, id, token, message, runAt)
	if ok {
		s.job.RetryWindowStartedAt = nil
	}
	return ok, err
}

func (s *checkpointRetryStore) RetryWithLease(ctx context.Context, id, token uuid.UUID, message string, runAt time.Time) (bool, error) {
	ok, err := s.RetryWithoutConsumingAttemptWithLease(ctx, id, token, message, runAt)
	if ok {
		s.job.Attempts++
	}
	return ok, err
}

func (s *checkpointRetryStore) DeadLetterWithLease(_ context.Context, _, _ uuid.UUID, _ string) (bool, error) {
	s.job.Status = models.JobStatusDeadLetter
	return true, nil
}

func TestCodeReviewAgentCheckpointResetsDependencyWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		checkpoint   func(models.CodeReviewPolicyConfig) error
		nextError    error
		disableReset bool
		loseLease    bool
	}{
		{name: "reviewers separate old rate limit from pending", checkpoint: codeReviewWaitingForReviewers, nextError: ghservice.ErrPullRequestMergeabilityPending},
		{name: "orchestrator separates old pending from unavailable", checkpoint: codeReviewWaitingForOrchestrator, nextError: &ghservice.GitHubAPIError{StatusCode: 503}},
		{name: "ordinary polling preserves deadline", checkpoint: codeReviewWaitingForReviewers, nextError: ghservice.ErrPullRequestMergeabilityPending, disableReset: true},
		{name: "lost lease preserves previous deadline", checkpoint: codeReviewWaitingForReviewers, loseLease: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oldStart := time.Now().Add(-3 * time.Hour)
			store := &checkpointRetryStore{job: models.Job{
				ID: uuid.New(), OrgID: uuid.New(), JobType: "run_code_review", MaxAttempts: 8,
				CreatedAt: oldStart, RetryWindowStartedAt: &oldStart,
			}, loseLease: tt.loseLease}
			checkpointErr := tt.checkpoint(models.CodeReviewPolicyConfig{})
			var checkpoint *RetryableError
			require.ErrorAs(t, checkpointErr, &checkpoint, "agent progress should defer the controller")
			if tt.disableReset {
				checkpoint.ResetRetryWindow = false
			}
			currentError := error(checkpoint)
			hooks, scheduled := 0, 0
			w := &Worker{jobs: store, logger: zerolog.Nop(), leaseDuration: time.Minute, renewInterval: time.Hour,
				handlers: map[string]JobHandler{"run_code_review": func(ctx context.Context, _ string, _ json.RawMessage) error {
					jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) { hooks++ })
					jobctx.RegisterRetryScheduledHook(ctx, func(context.Context, error, time.Time) { scheduled++ })
					return currentError
				}}}
			w.poll(context.Background())
			if tt.loseLease || tt.disableReset {
				require.Equal(t, &oldStart, store.job.RetryWindowStartedAt, "unapproved or unowned reset must preserve the deadline")
			} else {
				require.Nil(t, store.job.RetryWindowStartedAt, "successful agent checkpoint should clear the previous dependency window")
			}
			if tt.loseLease {
				require.Zero(t, scheduled, "lost lease must not publish a retry notification")
				require.Zero(t, hooks, "lost lease must not run terminal hooks")
				return
			}
			require.Equal(t, 0, store.job.Attempts, "agent wait should refund its claim attempt")
			services := &Services{PR: &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error { return tt.nextError }}}
			currentError = syncCodeReviewPullRequestState(context.Background(), services, zerolog.Nop(), runCodeReviewPayload{SessionID: uuid.New()})
			var retry *RetryableError
			require.ErrorAs(t, currentError, &retry, "next GitHub dependency error should stay retryable")
			require.False(t, retry.ResetRetryWindow, "dependency polling must never reset its own deadline")
			before := time.Now()
			w.poll(context.Background())
			if tt.disableReset {
				require.Equal(t, models.JobStatusDeadLetter, store.job.Status, "an unchanged expired window should still stop persistent dependency waits")
				require.Equal(t, 1, hooks, "deadline exhaustion should run terminal reconciliation")
				return
			}
			require.Equal(t, models.JobStatusPending, store.job.Status, "a new dependency episode after agent work should receive a grace retry")
			require.Zero(t, hooks, "prior dependency delays must not fail completed agent work")
			require.NotNil(t, store.job.RetryWindowStartedAt, "first dependency error should persist a fresh start")
			require.False(t, store.job.RetryWindowStartedAt.Before(before), "new retry budget must begin after agent progress")
			persisted := *store.job.RetryWindowStartedAt
			w.poll(context.Background())
			require.Equal(t, persisted, *store.job.RetryWindowStartedAt, "reclaims and ordinary retries must preserve the new deadline")
			store.job.RetryWindowStartedAt = &oldStart
			w.poll(context.Background())
			require.Equal(t, models.JobStatusDeadLetter, store.job.Status, "persistent failures after a checkpoint must remain bounded")
			require.Equal(t, 1, hooks, "eventual timeout should invoke terminal reconciliation once")
			require.True(t, errors.Is(currentError, tt.nextError), "wrapping must retain the original dependency failure")
		})
	}
}
