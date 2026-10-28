package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type confirmedCaptureSourceLifecycle struct {
	codeReviewLifecycleStub
	result codereviewsvc.ReviewRequestedResult
	cause  error
	ctx    context.Context
	input  codereviewsvc.ReviewChangedInput
}

func (s *confirmedCaptureSourceLifecycle) QueueCapturedIdentityReplacement(ctx context.Context, input codereviewsvc.ReviewChangedInput) (codereviewsvc.ReviewRequestedResult, error) {
	s.ctx, s.input = ctx, input
	return s.result, s.cause
}

func TestConfirmedCaptureSourceConsumerPreservesOwner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		wrongScope, failed bool
	}{
		{name: "exact source preservation is not replacement handoff"},
		{name: "wrong source disposition refuses before effects", wrongScope: true},
		{name: "source observation failure retains retry semantics", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create fixture forbidding all source retirement SQL")
			defer mock.Close()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), MetadataID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New()}
			metadata := models.CodeReviewSessionMetadata{ID: job.MetadataID, OrgID: job.OrgID, SessionID: job.SessionID, Status: models.CodeReviewSessionStatusRunning}
			lifecycle := &confirmedCaptureSourceLifecycle{result: codereviewsvc.ReviewRequestedResult{Processed: true, Reused: true, SessionID: job.SessionID, MetadataID: metadata.ID, IgnoredReason: codereviewsvc.CaptureIdentitySourceCompletionPendingReason}}
			if tt.wrongScope {
				lifecycle.result.MetadataID = uuid.New()
			}
			cause := errors.New("synthetic source receipt failure")
			if tt.failed {
				lifecycle.cause = cause
			}
			ctx := context.WithValue(context.Background(), confirmedCaptureContextKey{}, "exact-caller")
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(mock), Sessions: db.NewSessionStore(mock), Jobs: db.NewJobStore(mock)}
			err = supersedeCodeReviewForCapturedIdentity(ctx, stores, &Services{CodeReviewLifecycle: lifecycle}, zerolog.Nop(), job, metadata, codereviewsvc.AssessmentInputCaptureResult{}, &fullAssessmentQueuedIdentityChanged{})
			if tt.failed {
				require.ErrorIs(t, err, cause, "source observation error must retain its original dependency cause")
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "failed handoff recognition must remain a dependency retry rather than false retirement")
			} else if tt.wrongScope {
				require.Error(t, err, "a mismatched source preservation result is not authority to acknowledge or mutate this owner")
			} else {
				require.NoError(t, err, "confirmed original must finish this obsolete capture branch without a replacement handoff")
			}
			require.Same(t, ctx, lifecycle.ctx, "source recognition retains the original caller context")
			require.Equal(t, &metadata.ID, lifecycle.input.PriorMetadataID, "the known original metadata identity must be forwarded rather than rediscovered by session")
			require.Equal(t, job.SessionID, lifecycle.input.PriorSessionID, "the metadata hint remains independently bound to the original session")
			require.NoError(t, mock.ExpectationsWereMet(), "source preservation or its error must precede stale SQL, parent reconciliation, status jobs, and terminal effects")
		})
	}
}

type confirmedCaptureContextKey struct{}
