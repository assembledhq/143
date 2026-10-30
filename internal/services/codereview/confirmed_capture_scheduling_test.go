package codereview

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type confirmedCaptureSnapshotTrap struct {
	t            *testing.T
	allowPrepare bool
	prepared     int
}

func (s *confirmedCaptureSnapshotTrap) PrepareCodeReviewPullRequestSnapshot(context.Context, uuid.UUID, uuid.UUID) (ghservice.CodeReviewPullRequestSnapshotReader, error) {
	s.prepared++
	if !s.allowPrepare {
		s.t.Error("confirmed source fast path must not prepare provider work")
	}
	return func(context.Context, int) (ghservice.CodeReviewPullRequestSnapshot, error) {
		s.t.Error("confirmed source transaction recheck must precede provider reads")
		return ghservice.CodeReviewPullRequestSnapshot{}, errors.New("unexpected provider read")
	}, nil
}

func confirmedCaptureScheduleRows(state models.CodeReviewPRState) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "org_id", "repository_id", "pull_request_id", "generation", "automatic_paused", "head_sha", "base_sha", "base_ref", "is_draft", "snapshot_observed_at", "last_material_change_at", "first_pending_at", "last_agent_start_at", "eligible_at", "retry_at", "active_session_id", "active_assessment_id", "current_assessment_id", "pending_request_id", "pending_input", "state", "wait_reason", "created_at", "updated_at"}).AddRow(state.ID, state.OrgID, state.RepositoryID, state.PullRequestID, state.Generation, state.AutomaticPaused, state.HeadSHA, state.BaseSHA, state.BaseRef, state.IsDraft, state.SnapshotObservedAt, state.LastMaterialChangeAt, state.FirstPendingAt, state.LastAgentStartAt, state.EligibleAt, state.RetryAt, state.ActiveSessionID, state.ActiveAssessmentID, state.CurrentAssessmentID, state.PendingRequestID, state.PendingInput, state.State, state.WaitReason, state.CreatedAt, state.UpdatedAt)
}

func TestConfirmedCaptureTransactionRecheckRollsBackNoOp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		failed, starter bool
	}{
		{name: "confirmation found only by actual transaction reader"},
		{name: "transaction observation error refuses all mutations", failed: true},
		{name: "queued starter rechecks before provider and schedule mutation", starter: true},
		{name: "queued starter observation error rolls back whole no-op", starter: true, failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated existing schedule transaction fixture")
			defer mock.Close()
			input := newReviewChangedInput()
			input.PriorSessionID = uuid.New()
			input.ChangeReason = captureIdentityChangeReason
			metadataID, assessmentID := uuid.New(), uuid.New()
			input.PriorMetadataID = &metadataID
			source := models.CodeReviewSessionMetadata{ID: metadataID, OrgID: input.OrgID, SessionID: input.PriorSessionID, PullRequestID: input.PullRequestID, Status: models.CodeReviewSessionStatusRunning, TriggerSource: models.CodeReviewTriggerSourceAppReviewer}
			metadata := &metadataStub{getBySession: source}
			state := models.CodeReviewPRState{ID: uuid.New(), OrgID: input.OrgID, RepositoryID: input.RepositoryID, PullRequestID: input.PullRequestID, Generation: 7, HeadSHA: input.HeadSHA, BaseSHA: input.BaseSHA, State: models.CodeReviewScheduleRunning, ActiveSessionID: &input.PriorSessionID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`)).WithArgs("code_review_pr:" + input.OrgID.String() + ":" + input.PullRequestID.String()).WillReturnResult(pgxmock.NewResult("SELECT", 1))
			mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM pull_requests").WithArgs(input.OrgID, input.PullRequestID, input.RepositoryID).WillReturnRows(pgxmock.NewRows([]string{"owned"}).AddRow(true))
			mock.ExpectExec("INSERT INTO code_review_pr_state").WithArgs(input.OrgID, input.RepositoryID, input.PullRequestID).WillReturnResult(pgxmock.NewResult("INSERT", 0))
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM code_review_pr_state WHERE org_id=$1 AND pull_request_id=$2 FOR UPDATE`)).WithArgs(input.OrgID, input.PullRequestID).WillReturnRows(confirmedCaptureScheduleRows(state))
			sourceQuery := "SELECT id,org_id,session_id,pull_request_id,status,trigger_source FROM code_review_session_metadata"
			sourceRows := func() *pgxmock.Rows {
				return pgxmock.NewRows([]string{"id", "org_id", "session_id", "pull_request_id", "status", "trigger_source"}).AddRow(metadataID, input.OrgID, input.PriorSessionID, input.PullRequestID, source.Status, source.TriggerSource)
			}
			mock.ExpectQuery(sourceQuery).WithArgs(input.OrgID, input.PriorSessionID, input.PullRequestID, &metadataID).WillReturnRows(sourceRows())
			if !tt.starter {
				mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND pull_request_id=$3 AND id=$4 FOR UPDATE`)).WithArgs(input.OrgID, input.PriorSessionID, input.PullRequestID, metadataID).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(metadataID))
				mock.ExpectQuery(sourceQuery).WithArgs(input.OrgID, input.PriorSessionID, input.PullRequestID, &metadataID).WillReturnRows(sourceRows())
			}
			observation := mock.ExpectQuery(regexp.QuoteMeta(`SELECT a.id,COALESCE(a.xmin::text,'')`)).WithArgs(input.OrgID, metadataID, 8*1024*1024)
			cause := errors.New("synthetic transaction receipt storage failure")
			if tt.failed {
				observation.WillReturnError(cause)
			} else {
				receipt := []byte(`{"assessment_id":"` + assessmentID.String() + `","input_digest":"synthetic-digest","submitted_commit_sha":"synthetic-head","github_review_id":731}`)
				observation.WillReturnRows(pgxmock.NewRows([]string{"id", "version", "structural", "fits", "receipt", "digest", "head", "review_id"}).AddRow(&assessmentID, "opaque-version", true, true, receipt, "synthetic-digest", "synthetic-head", new(int64(731))))
			}
			mock.ExpectRollback()
			service := NewService(newPolicyStub(), metadata, &sessionStub{}, &jobStub{}, zerolog.Nop(), Config{})
			snapshots := &confirmedCaptureSnapshotTrap{t: t, allowPrepare: true}
			service.SetScheduling(db.NewCodeReviewScheduleStore(mock), snapshots)
			var result ReviewRequestedResult
			if tt.starter {
				result, err = service.HandleReviewChanged(context.Background(), input)
			} else {
				result, err = service.QueueCapturedIdentityReplacement(context.Background(), input)
			}
			if tt.failed {
				require.ErrorIs(t, err, cause, "transaction observation error cannot authorize force admission")
				require.Equal(t, ReviewRequestedResult{}, result, "failed transaction observation cannot expose a replacement")
			} else {
				require.NoError(t, err, "a positive exact transaction recheck should preserve source without committing the wrapper no-op")
				want := reusedReviewRequestedResult(source, source.TriggerSource)
				want.IgnoredReason = CaptureIdentitySourceCompletionPendingReason
				require.Equal(t, want, result, "transaction recheck must identify only the retained source, not a dispatched replacement")
			}
			require.Equal(t, 1, snapshots.prepared, "existing scheduling may prepare a reader outside the transaction but never invoke it")
			require.NoError(t, mock.ExpectationsWereMet(), "positive or failed recheck must roll back before joins, requests, generations, revocation, wake, schedule write, commit, or postcommit cleanup")
		})
	}
}
