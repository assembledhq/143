package handlers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestCodeReviewAssessmentBlockingReasonCodes(t *testing.T) {
	t.Parallel()
	safeguard := models.CodeReviewRiskReason{Code: models.CodeReviewRiskReasonSizeSafeguard, SizeSafeguard: &models.CodeReviewSizeSafeguardDecision{Outcome: models.CodeReviewSizeSafeguardWaived, Source: models.CodeReviewRiskTierSourceAuthoritative, Tier: models.CodeReviewRiskTierLow}}
	tests := []struct {
		name        string
		reasons     []models.CodeReviewRiskReason
		expected    []models.CodeReviewRiskReasonCode
		expectError bool
	}{
		{name: "no reasons", expected: []models.CodeReviewRiskReasonCode{}},
		{name: "only a safeguard record", reasons: []models.CodeReviewRiskReason{safeguard}, expected: []models.CodeReviewRiskReasonCode{}},
		{name: "blockers beside a safeguard record", reasons: []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonFilesLimitExceeded}, safeguard, {Code: models.CodeReviewRiskReasonBlockingFindings}}, expected: []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonFilesLimitExceeded, models.CodeReviewRiskReasonBlockingFindings}},
		{name: "unknown code", reasons: []models.CodeReviewRiskReason{{Code: "retired"}}, expectError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			codes, err := codeReviewAssessmentBlockingReasonCodes(tt.reasons)
			if tt.expectError {
				require.Error(t, err, "an unknown stored reason should still fail evidence loading")
				return
			}
			require.NoError(t, err, "valid stored reasons should convert")
			require.Equal(t, tt.expected, codes, "decision feedback should offer only blocking reason codes")
		})
	}
}
