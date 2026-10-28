package worker

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRunCodeReviewHandlerReconcilesRetainedBeforeLaterWork(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		queryFail bool
		events    []string
	}{
		{name: "actual retained selector failure stops the full handler", queryFail: true, events: []string{"run:1", "work-error:1"}},
		{name: "unknown retained read acknowledgement stops the full handler", events: []string{"run:1", "ack-error:1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "each actual-handler reconciliation case needs its own SQL expectations")
			t.Cleanup(mock.Close)
			job := legacyCodeReviewCPUJob(uuid.New(), uuid.New())
			job.RepositoryID, job.PullRequestID, job.PolicyID = uuid.New(), uuid.New(), uuid.New()
			job.PolicyVersion, job.HeadSHA = 1, "synthetic-retained-controller-head"
			cause := errors.New("synthetic original retained-selector boundary failure")
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(mock)}
			transactions := wireCodeReviewControllerCPUFixture(t, stores, mock)
			if tt.queryFail {
				mock.ExpectQuery(regexp.QuoteMeta(codeReviewCPUFixtureRetainedIDsSQL)).
					WithArgs(job.OrgID, job.SessionID, job.MetadataID, job.OutputKey).WillReturnError(cause)
			} else {
				expectCodeReviewNoRetainedReviewers(mock, job)
				transactions.acks = map[int]error{1: cause}
			}
			payload, err := json.Marshal(job)
			require.NoError(t, err, "serialize the complete original job through the real handler input")
			err = newRunCodeReviewHandler(stores, nil, zerolog.Nop())(context.Background(), models.JobTypeRunCodeReview, payload)
			require.ErrorIs(t, err, cause, "the actual top-level handler must preserve its retained reconciliation failure rather than skip to later work")
			require.ErrorContains(t, err, "reconcile retained reviewer commands", "the exact handler boundary must identify the failed reconciliation stage")
			require.Equal(t, 1, transactions.calls, "retained reconciliation must run before staged recovery, terminal fast paths or MarkRunning")
			require.Equal(t, tt.events, transactions.events, "no later owner, provider, publication or retry unit may run after failed reconciliation")
			require.NoError(t, mock.ExpectationsWereMet(), "the actual retained selector must be reached with original scope and no later metadata or provider queries")
		})
	}
}
