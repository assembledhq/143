package codereview

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestAssessmentDisputeReasonsOmitInformationalRecords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		raw             json.RawMessage
		expectedDetails []models.CodeReviewRiskReason
		expectedCodes   []models.CodeReviewRiskReasonCode
	}{
		{name: "no stored reasons", raw: nil, expectedCodes: []models.CodeReviewRiskReasonCode{}},
		{name: "empty stored reasons", raw: json.RawMessage(`[]`), expectedDetails: []models.CodeReviewRiskReason{}, expectedCodes: []models.CodeReviewRiskReasonCode{}},
		{
			name:            "safeguard record beside a blocker",
			raw:             json.RawMessage(`[{"code":"size_safeguard","size_safeguard":{"outcome":"waived","source":"authoritative","tier":"low","head_sha":"head"}},{"code":"blocked_path","subject":"deploy/prod.yml"}]`),
			expectedDetails: []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonBlockedPath, Subject: "deploy/prod.yml"}},
			expectedCodes:   []models.CodeReviewRiskReasonCode{models.CodeReviewRiskReasonBlockedPath},
		},
		{
			name:            "approved review with only a safeguard record",
			raw:             json.RawMessage(`[{"code":"size_safeguard","size_safeguard":{"outcome":"waived","source":"authoritative","tier":"medium","head_sha":"head"}}]`),
			expectedDetails: []models.CodeReviewRiskReason{},
			expectedCodes:   []models.CodeReviewRiskReasonCode{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			details, codes, err := assessmentDisputeReasons(tt.raw)
			require.NoError(t, err, "stored assessment reasons should decode")
			require.Equal(t, tt.expectedDetails, details, "dispute triage should not treat informational records as blockers")
			require.Equal(t, tt.expectedCodes, codes, "dispute reason codes should list only blockers")
		})
	}
}
