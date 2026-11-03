package worker

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Only this CPU fixture uses model tags to supply complete strict scanner rows.
// It neither implements production receipt proof nor emulates SQL settlement.
func confirmedRecoveryCPUModelRows(model any) *pgxmock.Rows {
	value := reflect.ValueOf(model)
	typ := value.Type()
	var columns []string
	var values []any
	for i := 0; i < typ.NumField(); i++ {
		column := typ.Field(i).Tag.Get("db")
		if column == "" || column == "-" {
			continue
		}
		columns = append(columns, column)
		values = append(values, value.Field(i).Interface())
	}
	return pgxmock.NewRows(columns).AddRow(values...)
}

func TestRecoverStagedFullAssessmentRespectsOperatorPauseBeforeRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		metadataReviewID bool
		terminalMetadata bool
		wrongOwner       bool
		detail           string
	}{
		{name: "confirmed original with metadata provider id pauses", metadataReviewID: true, detail: db.CodeReviewConfirmedCompletionOperatorRequired},
		{name: "confirmed original without metadata provider id pauses", detail: db.CodeReviewConfirmedCompletionOperatorRequired},
		{name: "pause precedes terminal orphan reconciliation", metadataReviewID: true, terminalMetadata: true, detail: db.CodeReviewConfirmedCompletionOperatorRequired},
		{name: "existing operator diagnostics remain paused", metadataReviewID: true, detail: db.CodeReviewPublicationOperatorRequired},
		{name: "operator marker cannot bypass exact metadata ownership", metadataReviewID: true, wrongOwner: true, detail: db.CodeReviewConfirmedCompletionOperatorRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "construct isolated read-only staged-recovery CPU fixture")
			defer mock.Close()
			_, job, a := newCodeReviewTerminalScopeFixture()
			origin, decision, acceptable, body, reviewID := models.CodeReviewResultExecuted, models.CodeReviewDecisionApproved, true, "Synthetic retained review", int64(731)
			a.ResultOrigin, a.Decision, a.Acceptable, a.RenderedBody = &origin, &decision, &acceptable, &body
			a.Status, a.PublicationState, a.CoverageComplete = models.CodeReviewAssessmentPublishing, models.CodeReviewPublicationConfirmed, true
			a.StructuredOutcome, a.FailureDetail, a.GitHubReviewID, a.SubmittedCommitSHA = json.RawMessage(`{}`), &tt.detail, &reviewID, &a.HeadSHA
			metadata := models.CodeReviewSessionMetadata{ID: job.MetadataID, OrgID: job.OrgID, SessionID: job.SessionID,
				RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, PolicyID: job.PolicyID,
				HeadSHA: job.HeadSHA, ReviewOutputKey: job.OutputKey, Status: models.CodeReviewSessionStatusRunning}
			if tt.metadataReviewID {
				metadata.GitHubReviewID = &reviewID
			}
			if tt.terminalMetadata {
				metadata.Status = models.CodeReviewSessionStatusFailed
			}
			if tt.wrongOwner {
				metadata.ID = uuid.New()
			}
			mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").
				WithArgs(job.OrgID, job.SessionID).WillReturnRows(confirmedRecoveryCPUModelRows(a))
			mock.ExpectQuery("FROM code_review_session_metadata").
				WithArgs(job.OrgID, job.SessionID).
				WillReturnRows(confirmedRecoveryCPUModelRows(metadata))
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(mock), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}
			handled, err := recoverStagedFullAssessment(context.Background(), stores, &Services{}, job)
			require.True(t, handled, "a staged original assessment remains the recovery entry's owner")
			if tt.wrongOwner {
				require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "pause must preserve the existing exact metadata identity refusal")
			} else {
				require.ErrorIs(t, err, errCodeReviewPublicationPaused, "a manually queued wake must not bypass the committed operator pause via a metadata provider id")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "paused recovery must perform no reconciliation write, provider call, transaction, or completion effect")
		})
	}
}
