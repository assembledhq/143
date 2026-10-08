package codereview

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildAssessmentCapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*models.CodeReviewAssessmentCapture, *ReviewInputCapture)
		valid  bool
	}{
		{"complete capture", func(*models.CodeReviewAssessmentCapture, *ReviewInputCapture) {}, true},
		{"wrong PR identity", func(id *models.CodeReviewAssessmentCapture, _ *ReviewInputCapture) { id.PullRequestID = uuid.New() }, false},
		{"unknown file provenance", func(_ *models.CodeReviewAssessmentCapture, in *ReviewInputCapture) { in.Code.FilesComplete = false }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := reviewTestCapture()
			identity := models.CodeReviewAssessmentCapture{ID: uuid.New(), OrgID: input.Code.OrgID, RepositoryID: input.Code.RepositoryID, PullRequestID: input.Code.PullRequestID, PolicyID: input.Contract.PolicyID, SessionID: uuid.New(), Generation: 1, ReviewScope: models.CodeReviewScopeFull, RouteReason: models.CodeReviewRouteInitialFull, PublicationKey: uuid.NewString()}
			tt.change(&identity, &input)
			got, err := BuildAssessmentCapture(identity, input)
			if !tt.valid {
				require.Error(t, err, "incomplete or mismatched source should fail capture")
				return
			}
			require.NoError(t, err, "complete input should capture")
			require.Equal(t, input.Code.HeadSHA, got.HeadSHA, "captured head should come from verified code input")
			require.NotEmpty(t, got.InputManifest, "capture should retain the versioned source manifest")
			require.NotEmpty(t, got.InputDigest, "capture should bind all named components")
		})
	}
}
