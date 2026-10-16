package worker

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestPrepareFullAssessmentReportsQueuedIdentityDrift(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		change   func(*codereviewsvc.AssessmentInputCaptureResult)
		expected []string
	}{
		{name: "policy save", change: func(c *codereviewsvc.AssessmentInputCaptureResult) { c.Policy.ID = uuid.New(); c.Policy.Version++ }, expected: []string{"policy_id", "policy_version"}},
		{name: "head changed during capture", change: func(c *codereviewsvc.AssessmentInputCaptureResult) { c.Snapshot.HeadSHA = "new-head" }, expected: []string{"head_sha"}},
		{name: "base changed during capture", change: func(c *codereviewsvc.AssessmentInputCaptureResult) { c.Snapshot.BaseSHA = "new-base" }, expected: []string{"base_sha"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), PolicyID: uuid.New(), PolicyVersion: 4, HeadSHA: "head"}
			metadata := models.CodeReviewSessionMetadata{BaseSHA: "base"}
			captured := codereviewsvc.AssessmentInputCaptureResult{Policy: models.CodeReviewPolicyRecord{ID: job.PolicyID, Version: job.PolicyVersion}, Snapshot: ghservice.CodeReviewPullRequestSnapshot{HeadSHA: job.HeadSHA, BaseSHA: metadata.BaseSHA}}
			tt.change(&captured)
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated queued assessment lookup")
			defer mock.Close()
			mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").WithArgs(job.OrgID, job.SessionID).WillReturnError(pgx.ErrNoRows)
			capture := &assessmentFreshnessCapture{result: captured}
			assessment, current, err := prepareFullAssessment(context.Background(), &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}, &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture}, job, metadata)
			var changed *fullAssessmentQueuedIdentityChanged
			require.ErrorAs(t, err, &changed, "identity drift should be recoverable as superseded input")
			require.Equal(t, tt.expected, changed.Fields, "drift should identify only the fields that changed")
			require.Nil(t, assessment, "drift must not persist an assessment under the obsolete queued policy")
			require.Equal(t, &captured, current, "replacement admission should retain the authoritative provider capture")
			require.Equal(t, 4, job.PolicyVersion, "capture recovery must not mutate the queued policy version")
			require.NoError(t, mock.ExpectationsWereMet(), "drift must stop before any assessment insert or reviewer fanout")
		})
	}
}
