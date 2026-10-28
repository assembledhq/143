package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
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
	job          models.Job
	loseLease    bool
	genericCalls int
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
	s.genericCalls++
	return false, errors.New("review checkpoint unexpectedly used generic retirement")
}

type checkpointTerminalRequest struct {
	orgID, jobID, lockToken uuid.UUID
	failure                 db.CodeReviewTerminalFailure
}

// This CPU-only dependency records the terminal decision, not persistence. It
// cannot manufacture a sealed commit outcome or release postcommit hooks.
// Positive actual retirement remains covered by the companion Worker.poll PG
// matrices, including their duration-exhaustion rows.
type checkpointTerminalRecorder struct{ requests []checkpointTerminalRequest }

func (s *checkpointTerminalRecorder) DeadLetterCodeReviewWithLease(_ context.Context, orgID, jobID, lockToken uuid.UUID, failure db.CodeReviewTerminalFailure) (db.CodeReviewTerminalOutcome, error) {
	s.requests = append(s.requests, checkpointTerminalRequest{orgID, jobID, lockToken, failure})
	return db.CodeReviewTerminalOutcome{}, errors.New("synthetic retirement result remains unconfirmed")
}

func checkpointMergeabilityTerminalFailure() db.CodeReviewTerminalFailure {
	return db.CodeReviewTerminalFailure{
		JobError:  "retryable job timed out after 2h0m0s: pull request mergeability is still being checked by GitHub",
		Reason:    "code review job failed: retryable job timed out after 2h0m0s: pull request mergeability is still being checked by GitHub",
		Code:      models.CodeReviewStatusCodeWorkerFailed,
		Message:   "The review could not recover automatically. Retry the review to start a fresh attempt.",
		Retryable: true,
	}
}

func TestCodeReviewAgentCheckpointResetsDependencyWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                     string
		checkpoint               func(models.CodeReviewPolicyConfig) error
		nextError                error
		disableReset             bool
		loseLease                bool
		expectedFailure          db.CodeReviewTerminalFailure
		expectedAttemptsPerRetry int
	}{
		{name: "reviewers separate old rate limit from pending", checkpoint: codeReviewWaitingForReviewers, nextError: ghservice.ErrPullRequestMergeabilityPending, expectedFailure: checkpointMergeabilityTerminalFailure()},
		{name: "orchestrator separates old pending from unavailable", checkpoint: codeReviewWaitingForOrchestrator,
			nextError: &ghservice.GitHubAPIError{Method: "GET", Path: "/synthetic", StatusCode: 503, Body: []byte("synthetic unavailable")}, expectedAttemptsPerRetry: 1,
			expectedFailure: db.CodeReviewTerminalFailure{
				JobError: "retryable job timed out after 2h0m0s: sync code review pull request state: GitHub API GET /synthetic returned 503: synthetic unavailable",
				Reason:   "code review job failed: GitHub API GET /synthetic returned 503", Code: models.CodeReviewStatusCodeGitHubUnavailable,
				Message: "GitHub remained unavailable until automatic retries expired. Retry the review to start a fresh attempt.", Retryable: true,
			}},
		{name: "ordinary polling preserves deadline", checkpoint: codeReviewWaitingForReviewers, nextError: ghservice.ErrPullRequestMergeabilityPending, disableReset: true, expectedFailure: checkpointMergeabilityTerminalFailure()},
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
			terminal := &checkpointTerminalRecorder{}
			var claimedToken uuid.UUID
			w := &Worker{jobs: store, logger: zerolog.Nop(), leaseDuration: time.Minute, renewInterval: time.Hour,
				handlers: map[string]JobHandler{"run_code_review": func(ctx context.Context, _ string, _ json.RawMessage) error {
					var hasToken bool
					claimedToken, hasToken = jobctx.LockTokenFromContext(ctx)
					require.True(t, hasToken, "each actual poll must retain its independently observed handler claim token")
					jobctx.RegisterDeadLetterHook(ctx, func(context.Context, error) { hooks++ })
					jobctx.RegisterRetryScheduledHook(ctx, func(context.Context, error, time.Time) { scheduled++ })
					return currentError
				}}}
			WithCodeReviewTerminalStore(terminal)(w)
			assertUnconfirmedRetirement := func(attempts, retries int) {
				t.Helper()
				require.Equal(t, []checkpointTerminalRequest{{store.job.OrgID, store.job.ID, claimedToken, tt.expectedFailure}}, terminal.requests, "deadline exhaustion must submit one complete typed request for the exact original job and last claim")
				expectedJob := models.Job{ID: store.job.ID, OrgID: store.job.OrgID, JobType: models.JobTypeRunCodeReview, MaxAttempts: 8,
					CreatedAt: oldStart, RetryWindowStartedAt: &oldStart, LockToken: &claimedToken, Status: models.JobStatusRunning, Attempts: attempts}
				require.Equal(t, expectedJob, store.job, "unconfirmed retirement must retain every claimed job field instead of pretending persistence succeeded")
				require.Equal(t, retries, scheduled, "exhaustion must not schedule another retry or refund its final claim")
				require.Zero(t, hooks, "an unconfirmed terminal request must not invoke any cleanup hook")
				require.Zero(t, store.genericCalls, "review retirement cannot fall back to the ordinary queue store")
				require.True(t, errors.Is(currentError, tt.nextError), "dependency wrapping must retain the original cause through exhaustion")
			}
			w.poll(context.Background())
			require.Empty(t, terminal.requests, "an agent-progress checkpoint is not a terminal dependency decision")
			require.Zero(t, store.genericCalls, "checkpoint retry must never use ordinary terminal persistence")
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
			require.Equal(t, 1, scheduled, "the owned checkpoint must publish exactly one scheduled retry")
			services := &Services{PR: &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error { return tt.nextError }}}
			currentError = syncCodeReviewPullRequestState(context.Background(), services, zerolog.Nop(), runCodeReviewPayload{SessionID: uuid.New()})
			var retry *RetryableError
			require.ErrorAs(t, currentError, &retry, "next GitHub dependency error should stay retryable")
			require.False(t, retry.ResetRetryWindow, "dependency polling must never reset its own deadline")
			before := time.Now()
			w.poll(context.Background())
			if tt.disableReset {
				assertUnconfirmedRetirement(1, 1)
				return
			}
			require.Equal(t, models.JobStatusPending, store.job.Status, "a new dependency episode after agent work should receive a grace retry")
			require.Equal(t, tt.expectedAttemptsPerRetry, store.job.Attempts, "only consuming provider retries may spend the original attempt budget")
			require.Empty(t, terminal.requests, "the fresh dependency episode must not request terminal persistence")
			require.Zero(t, hooks, "prior dependency delays must not fail completed agent work")
			require.NotNil(t, store.job.RetryWindowStartedAt, "first dependency error should persist a fresh start")
			require.False(t, store.job.RetryWindowStartedAt.Before(before), "new retry budget must begin after agent progress")
			persisted := *store.job.RetryWindowStartedAt
			w.poll(context.Background())
			require.Equal(t, persisted, *store.job.RetryWindowStartedAt, "reclaims and ordinary retries must preserve the new deadline")
			require.Equal(t, 2*tt.expectedAttemptsPerRetry, store.job.Attempts, "ordinary reclaims must preserve consuming versus nonconsuming attempt semantics")
			require.Empty(t, terminal.requests, "ordinary polling inside the original budget must stay nonterminal")
			store.job.RetryWindowStartedAt = &oldStart
			w.poll(context.Background())
			assertUnconfirmedRetirement(2*tt.expectedAttemptsPerRetry+1, 3)
		})
	}
}
