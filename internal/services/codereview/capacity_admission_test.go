package codereview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewCapacityPrecedesAttemptClock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		err   error
		reuse bool
	}{
		{name: "no available sandbox"},
		{name: "inventory unavailable", err: errors.New("inventory unavailable")},
		{name: "completed reuse needs no new sandbox", reuse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			input := newReviewRequestedInput(nil)
			metadata := &metadataStub{}
			if tt.reuse {
				metadata.latest = models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: uuid.New(), Status: models.CodeReviewSessionStatusCompleted}
			}
			sessions, jobs := &sessionStub{}, &jobStub{}
			service := NewService(newPolicyStub(), metadata, sessions, jobs, zerolog.Nop(), Config{})
			service.SetSandboxAdmission()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create transactional admission mock")
			defer mock.Close()
			mock.ExpectBegin()
			tx, err := mock.Begin(ctx)
			require.NoError(t, err, "begin isolated review admission")
			service.admissionTx = tx
			if !tt.reuse {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(pgxmock.NewResult("SELECT", 1))
				query := mock.ExpectQuery("WITH counts").WithArgs(input.OrgID, uuid.Nil)
				if tt.err != nil {
					query.WillReturnError(tt.err)
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(false))
				}
			}
			result, err := service.startReview(ctx, input, reviewStartOptions{triggerSource: models.CodeReviewTriggerSourceAppReviewer})
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err, "unverified inventory must stop review admission")
			} else {
				require.NoError(t, err, "capacity deferral and completed reuse are normal outcomes")
			}
			if tt.reuse {
				require.Equal(t, metadata.latest.SessionID, result.SessionID, "completed reuse should preserve the existing immutable attempt")
			} else if tt.err == nil {
				require.Equal(t, ReviewRequestedResult{Processed: true, Deferred: true, TriggerSource: models.CodeReviewTriggerSourceAppReviewer, IgnoredReason: "sandbox_capacity"}, result, "capacity wait must not invent a session")
			}
			require.Equal(t, []int{0, 0, 0}, []int{sessions.createCalls, metadata.createCalls, jobs.enqueueCalls}, "no admission must mean no new attempt, deadline, or runnable review")
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback(ctx), "release test admission lock")
			require.NoError(t, mock.ExpectationsWereMet(), "every transaction expectation should be met")
		})
	}
}

func TestRetryReviewCapacityPreservesSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	org, repo, pr, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	source := models.CodeReviewSessionMetadata{ID: uuid.New(), OrgID: org, RepositoryID: repo, PullRequestID: pr, SessionID: sessionID, PolicyID: uuid.New(), Status: models.CodeReviewSessionStatusFailed, RetryableFailure: true, HeadSHA: "head", BaseSHA: "base", CreatedAt: time.Now().Add(-time.Hour)}
	metadata := &metadataStub{latest: source, submitted: source}
	sessions, jobs := &sessionStub{}, &jobStub{}
	service := NewService(newPolicyStub(), metadata, sessions, jobs, zerolog.Nop(), Config{})
	service.SetSandboxAdmission()
	service.SetRetryDependencies(&pullRequestStub{result: models.PullRequest{ID: pr, OrgID: org, Status: models.PullRequestStatusOpen, GitHubRepo: "acme/api"}}, nil)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create retry admission transaction")
	defer mock.Close()
	mock.ExpectBegin()
	tx, err := mock.Begin(ctx)
	require.NoError(t, err, "begin retry admission")
	service.admissionTx = tx
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("WITH counts").WithArgs(org, uuid.Nil).WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(false))
	result, err := service.retryReview(ctx, RetryReviewInput{OrgID: org, SessionID: sessionID}, &ghservice.CodeReviewPullRequestSnapshot{State: "open", HeadSHA: "head", BaseSHA: "base"})
	var conflict *RetryReviewConflictError
	require.ErrorAs(t, err, &conflict, "capacity waits should return a typed conflict to cadence callers")
	require.Equal(t, RetryReviewConflictCapacity, conflict.Code, "capacity is distinct from a newer attempt or provider failure")
	require.Equal(t, RetryReviewResult{}, result, "a deferred retry cannot return a replacement identity")
	require.Equal(t, source, metadata.latest, "capacity waits must preserve immutable source status, clock, and receipt fields")
	require.Nil(t, metadata.supersededBy, "capacity waits must not link or supersede the failed source")
	require.Equal(t, []int{0, 0, 0, 0}, []int{sessions.createCalls, metadata.createCalls, metadata.failCalls, jobs.enqueueCalls}, "a deferred retry must create no attempt clock and retire no source")
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback(ctx), "release retry admission transaction")
	require.NoError(t, mock.ExpectationsWereMet(), "retry should execute only admission reads")
}
