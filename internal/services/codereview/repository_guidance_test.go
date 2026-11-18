package codereview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRepositoryGuidanceRecheck(t *testing.T) {
	t.Parallel()
	original := models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "Original guidance"}
	revised := models.CodeReviewRepositoryGuidance{Branch: "master", Present: true, BlobSHA: strings.Repeat("b", 40), Content: "Revised guidance"}
	absent := models.CodeReviewRepositoryGuidance{Branch: "master"}
	nested := original
	nested.NestedFiles = []models.CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: strings.Repeat("c", 40), Content: "Check request boundaries."}}
	nestedRevised := original
	nestedRevised.NestedFiles = []models.CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: strings.Repeat("d", 40), Content: "Check authorization boundaries."}}
	nestedMoved := original
	nestedMoved.NestedFiles = []models.CodeReviewGuidanceFile{{Path: "web/REVIEW.md", BlobSHA: strings.Repeat("c", 40), Content: "Check request boundaries."}}
	tests := []struct {
		name          string
		before, after *models.CodeReviewRepositoryGuidance
		want          RecheckPlan
	}{
		{"unchanged file", &original, &original, RecheckPlan{RecheckRouteReuse, RecheckReasonUnchanged}},
		{"updated file same PR head", &original, &revised, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"added file", &absent, &original, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"removed file", &original, &absent, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"historical capture", nil, &absent, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"unchanged nested file", &nested, &nested, RecheckPlan{RecheckRouteReuse, RecheckReasonUnchanged}},
		{"added nested file", &original, &nested, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"updated nested file", &nested, &nestedRevised, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"removed nested file", &nested, &original, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
		{"moved nested file", &nested, &nestedMoved, RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			capture := reviewTestCapture()
			capture.Contract.RepositoryGuidance = tt.before
			before, err := BuildReviewInputManifest(capture)
			require.NoError(t, err, "baseline should capture")
			capture.Contract.RepositoryGuidance = tt.after
			after, err := BuildReviewInputManifest(capture)
			require.NoError(t, err, "current guidance should capture")
			require.Equal(t, before.CodeDigest, after.CodeDigest, "guidance must not alter the PR source identity")
			require.Equal(t, tt.want, PlanReviewRecheck(RecheckPlanInput{Current: &after, Baseline: reviewTestBaseline(before), Previous: &RecheckPrevious{Inputs: before, Completed: true, EvidenceValidated: true}}), "changed guidance must invalidate otherwise reusable reviews")
			raw, err := json.Marshal(after)
			require.NoError(t, err, "guidance manifest should serialize")
			var restored ReviewInputManifest
			require.NoError(t, json.Unmarshal(raw, &restored), "guidance manifest should restore")
			require.Equal(t, after, restored, "persisted guidance must preserve exact bytes and provenance")
			require.NoError(t, ValidateReviewInputManifest(restored), "persisted manifest should validate")
		})
	}
}

func TestRepositoryGuidanceCaptureDoesNotAliasCaller(t *testing.T) {
	t.Parallel()
	capture := reviewTestCapture()
	capture.Contract.RepositoryGuidance = &models.CodeReviewRepositoryGuidance{
		Branch: "master", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "Captured guidance",
		NestedFiles: []models.CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: strings.Repeat("b", 40), Content: "Captured API guidance"}},
	}
	manifest, err := BuildReviewInputManifest(capture)
	require.NoError(t, err, "guidance capture should build")
	capture.Contract.RepositoryGuidance.Content = "Later caller mutation"
	capture.Contract.RepositoryGuidance.NestedFiles[0].Content = "Later nested mutation"
	require.Equal(t, "Captured guidance", manifest.Contract.RepositoryGuidance.Content, "caller mutation must not alter immutable captured instructions")
	require.Equal(t, []models.CodeReviewGuidanceFile{{Path: "api/REVIEW.md", BlobSHA: strings.Repeat("b", 40), Content: "Captured API guidance"}}, manifest.Contract.RepositoryGuidance.NestedFiles, "nested instruction slices must not alias caller-owned storage")
	require.NoError(t, ValidateReviewInputManifest(manifest), "immutable manifest must retain valid digests")
}
