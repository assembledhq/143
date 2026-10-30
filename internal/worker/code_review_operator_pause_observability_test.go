package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRunCodeReviewHandlerReportsOperatorPause(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		publication      models.CodeReviewPublicationState
		metadataReviewID bool
		wrongOwner       bool
		detail           string
	}{
		{name: "confirmed exhausted completion with metadata provider id", publication: models.CodeReviewPublicationConfirmed,
			metadataReviewID: true, detail: db.CodeReviewConfirmedCompletionOperatorRequired},
		{name: "confirmed exhausted completion without metadata provider id", publication: models.CodeReviewPublicationConfirmed,
			detail: db.CodeReviewConfirmedCompletionOperatorRequired + ":synthetic-private-diagnostic"},
		{name: "existing uncertain operator pause keeps its generic reason", publication: models.CodeReviewPublicationUncertain,
			detail: db.CodeReviewPublicationOperatorRequired + ":synthetic-private-diagnostic"},
		{name: "identity refusal is not reported as an operator pause", publication: models.CodeReviewPublicationConfirmed,
			metadataReviewID: true, wrongOwner: true, detail: db.CodeReviewConfirmedCompletionOperatorRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated exact-reader fixtures for the actual controller recovery entry")
			defer mock.Close()
			_, job, assessment := newCodeReviewTerminalScopeFixture()
			origin, decision, acceptable := models.CodeReviewResultExecuted, models.CodeReviewDecisionApproved, true
			body, reviewID := "Synthetic private staged review body", int64(731)
			assessment.ResultOrigin, assessment.Decision, assessment.Acceptable, assessment.RenderedBody = &origin, &decision, &acceptable, &body
			assessment.Status, assessment.PublicationState, assessment.CoverageComplete = models.CodeReviewAssessmentPublishing, tt.publication, true
			assessment.StructuredOutcome = json.RawMessage(`{"synthetic":"private structured outcome"}`)
			assessment.PublicationReceipt = json.RawMessage(`{"synthetic":"private publication receipt"}`)
			assessment.FailureDetail, assessment.GitHubReviewID, assessment.SubmittedCommitSHA = &tt.detail, &reviewID, &assessment.HeadSHA
			metadata := models.CodeReviewSessionMetadata{ID: job.MetadataID, OrgID: job.OrgID, SessionID: job.SessionID,
				RepositoryID: job.RepositoryID, PullRequestID: job.PullRequestID, PolicyID: job.PolicyID,
				HeadSHA: job.HeadSHA, ReviewOutputKey: job.OutputKey, Status: models.CodeReviewSessionStatusRunning}
			if tt.metadataReviewID {
				metadata.GitHubReviewID = &reviewID
			}
			if tt.wrongOwner {
				metadata.ID = uuid.New()
			}
			expectCodeReviewNoRetainedReviewers(mock, job)
			mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").
				WithArgs(job.OrgID, job.SessionID).WillReturnRows(confirmedRecoveryCPUModelRows(assessment))
			mock.ExpectQuery("FROM code_review_session_metadata").WithArgs(job.OrgID, job.SessionID).
				WillReturnRows(confirmedRecoveryCPUModelRows(metadata))
			var output bytes.Buffer
			payload, err := json.Marshal(job)
			require.NoError(t, err, "serialize the original controller job through its real handler input")
			stores := &Stores{CodeReviews: db.NewCodeReviewStore(mock), CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}
			wireCodeReviewControllerCPUFixture(t, stores, mock)
			err = newRunCodeReviewHandler(stores, &Services{}, zerolog.New(&output))(context.Background(), models.JobTypeRunCodeReview, payload)
			if tt.wrongOwner {
				require.ErrorIs(t, err, db.ErrCodeReviewAssessmentState, "a failed original-owner check must remain a failure rather than become a successful pause")
			} else {
				require.NoError(t, err, "the existing durable operator pause must still stop the controller without retrying or failing the review")
			}
			warnings := make([]map[string]any, 0)
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				if line == "" {
					continue
				}
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event), "operator observability must remain structured JSON")
				if event["level"] == "warn" {
					warnings = append(warnings, event)
				}
			}
			expected := make([]map[string]any, 0)
			if !tt.wrongOwner {
				expected = append(expected, map[string]any{"level": "warn", "org_id": job.OrgID.String(), "session_id": job.SessionID.String(),
					"review_head_sha": job.HeadSHA, "review_id": job.MetadataID.String(), "reason": "operator_reconciliation_required",
					"message": "code review publication paused for operator reconciliation"})
			}
			require.Equal(t, expected, warnings, "only the actual recovered operator pause must emit exactly one warning with a fixed safe reason and the existing controller identity")
			for _, private := range []string{body, string(assessment.PublicationReceipt), string(assessment.StructuredOutcome), tt.detail, "synthetic-private-diagnostic", "failure_detail", "publication_receipt", "raw_output"} {
				require.NotContains(t, output.String(), private, "operator diagnostics must never copy private receipt, staged output or stored failure-detail material into the warning")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "reporting the real pause must require no additional query, provider request, settlement, completion or retry")
		})
	}
}
