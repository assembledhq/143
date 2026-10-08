package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type publicationRecoveryDB struct {
	db.DBTX
	exec func(context.Context) (pgconn.CommandTag, error)
}

func (d publicationRecoveryDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return d.exec(ctx)
}

func TestReconcileRejectedCodeReviewPublication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		cancelled  bool
		expired    bool
		priorSend  bool
		ambiguous  bool
		fenceFails bool
		dbFails    bool
	}{
		{name: "normal rejection"},
		{name: "caller cancelled after rejection", cancelled: true},
		{name: "publication deadline expired after rejection", expired: true},
		{name: "earlier uncertain send is preserved", priorSend: true},
		{name: "ambiguous response is preserved", ambiguous: true},
		{name: "recovery fence failure preserves both errors", fenceFails: true},
		{name: "database failure preserves both errors", dbFails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jobID := uuid.New()
			ctx := jobctx.WithJobID(context.Background(), jobID)
			if tt.expired {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				if tt.cancelled {
					cancel()
				}
			}
			apiErr := &ghservice.GitHubAPIError{StatusCode: http.StatusUnprocessableEntity}
			submitErr := fmt.Errorf("%w: %w", codereviewsvc.ErrReviewPublicationRejected, apiErr)
			if tt.ambiguous {
				submitErr = errors.New("response lost")
			}
			dbErr := errors.New("database unavailable")
			var recoveryCtx context.Context
			backend := publicationRecoveryDB{exec: func(writeCtx context.Context) (pgconn.CommandTag, error) {
				recoveryCtx = writeCtx
				require.NoError(t, writeCtx.Err(), "persisting a received rejection must survive caller cancellation and deadline expiry")
				deadline, bounded := writeCtx.Deadline()
				require.True(t, bounded, "detached recovery must have its own bounded deadline")
				require.InDelta(t, 5, time.Until(deadline).Seconds(), 1, "recovery should use a short independent five-second timeout")
				actualJob, present := jobctx.JobIDFromContext(writeCtx)
				require.True(t, present, "detached recovery should preserve diagnostic context")
				require.Equal(t, jobID, actualJob, "detached recovery should retain the originating job")
				if tt.dbFails {
					return pgconn.CommandTag{}, dbErr
				}
				if tt.fenceFails {
					return pgconn.NewCommandTag("UPDATE 0"), nil
				}
				return pgconn.NewCommandTag("UPDATE 1"), nil
			}}
			stores := &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(backend)}
			err := reconcileRejectedCodeReviewPublication(ctx, stores, models.CodeReviewAssessment{}, !tt.priorSend, submitErr)
			require.ErrorIs(t, err, submitErr, "original publication error must survive recovery")
			if tt.priorSend || tt.ambiguous {
				require.Nil(t, recoveryCtx, "uncertain external outcomes must never trigger the restore write")
				return
			}
			require.NotNil(t, recoveryCtx, "definite rejection should attempt durable recovery")
			require.ErrorIs(t, recoveryCtx.Err(), context.Canceled, "release the detached recovery timeout after the write")
			require.ErrorIs(t, err, apiErr, "GitHub retry classification must retain the provider error")
			if tt.fenceFails {
				require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "surface the failed state fence without suppressing the rejection")
			}
			if tt.dbFails {
				require.ErrorIs(t, err, dbErr, "surface the persistence failure without suppressing the rejection")
			}
		})
	}
}
