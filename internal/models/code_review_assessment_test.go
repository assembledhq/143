package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewAssessmentEnums(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{
		{"full scope", func() error { return CodeReviewScopeFull.Validate() }, true},
		{"evidence scope", func() error { return CodeReviewScopeEvidenceOnly.Validate() }, true},
		{"unknown scope", func() error { return CodeReviewScope("partial").Validate() }, false},
		{"visual route", func() error { return CodeReviewRouteVisualChanged.Validate() }, true},
		{"evidence route", func() error { return CodeReviewRouteEvidenceChanged.Validate() }, true},
		{"checks route", func() error { return CodeReviewRouteChecksChanged.Validate() }, true},
		{"unchanged evidence route", func() error { return CodeReviewRouteNoEvidenceChange.Validate() }, true},
		{"unknown route", func() error { return CodeReviewAssessmentRouteReason("shortcut").Validate() }, false},
		{"reserved assessment", func() error { return CodeReviewAssessmentReserved.Validate() }, true},
		{"completed assessment", func() error { return CodeReviewAssessmentCompleted.Validate() }, true},
		{"unknown assessment state", func() error { return CodeReviewAssessmentStatus("done").Validate() }, false},
		{"executed result", func() error { return CodeReviewResultExecuted.Validate() }, true},
		{"reused result", func() error { return CodeReviewResultReused.Validate() }, true},
		{"evidence result", func() error { return CodeReviewResultEvidenceOnly.Validate() }, true},
		{"unknown result", func() error { return CodeReviewResultOrigin("inferred").Validate() }, false},
		{"reserved publication", func() error { return CodeReviewPublicationReserved.Validate() }, true},
		{"confirmed publication", func() error { return CodeReviewPublicationConfirmed.Validate() }, true},
		{"not required publication", func() error { return CodeReviewPublicationNotRequired.Validate() }, true},
		{"unknown publication", func() error { return CodeReviewPublicationState("sent").Validate() }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.valid {
				require.NoError(t, err, "known assessment enum value should validate")
			} else {
				require.Error(t, err, "unknown assessment enum value should be rejected")
			}
		})
	}
}
