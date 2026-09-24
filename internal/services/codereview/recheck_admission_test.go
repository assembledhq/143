package codereview

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestApplicableDescriptionRequirements(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	tests := []struct {
		name     string
		files    []PullRequestFile
		expected []string
	}{
		{"small backend", []PullRequestFile{{Filename: "internal/api/review.go", Additions: 2}}, []string{"description"}},
		{"frontend visual", []PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}, []string{"description", "ui_evidence"}},
		{"nontrivial", []PullRequestFile{{Filename: "internal/a.go", Additions: 20}, {Filename: "internal/b.go", Additions: 12}}, []string{"description", "testing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requirements := ApplicableDescriptionRequirements(policy, tt.files)
			keys := make([]string, 0, len(requirements))
			for _, r := range requirements {
				keys = append(keys, r.Key)
			}
			require.Equal(t, tt.expected, keys, "admission should use the same applicable requirement set as full review")
		})
	}
}

func TestPendingForcedFull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  json.RawMessage
		want bool
		bad  bool
	}{
		{"absent", nil, false, false},
		{"ordinary recheck", json.RawMessage(`{"mode":"recheck"}`), false, false},
		{"force fresh", json.RawMessage(`{"mode":"force_fresh"}`), true, false},
		{"forced full fallback", json.RawMessage(`{"mode":"recheck","force":true}`), true, false},
		{"malformed pending state", json.RawMessage(`{`), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pendingForcedFull(tt.raw)
			if tt.bad {
				require.Error(t, err, "malformed pending state must fail closed")
				return
			}
			require.NoError(t, err, "valid pending state should decode")
			require.Equal(t, tt.want, got, "forced full intent should remain protected from recheck admission")
		})
	}
}

func TestBaselineForPlanning(t *testing.T) {
	t.Parallel()
	manifest, err := BuildReviewInputManifest(reviewTestCapture())
	require.NoError(t, err, "baseline fixture should build")
	raw, err := json.Marshal(manifest)
	require.NoError(t, err, "baseline manifest should marshal")
	policy := models.DefaultCodeReviewPolicyConfig()
	files := []PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	reasons := []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonDescriptionFailed}}
	reasonsRaw, err := json.Marshal(reasons)
	require.NoError(t, err, "risk reasons should marshal")
	tests := []struct {
		name             string
		outcome          any
		expectedCoverage bool
		expectedMissing  []RecheckMissingRequirement
	}{
		{"complete visual blocker", map[string]any{"description_assessments": []map[string]string{{"key": "description", "status": "satisfied"}, {"key": "ui_evidence", "status": "missing"}}, "risk_reasons": reasons, "coverage_complete": true}, true, []RecheckMissingRequirement{{ID: "ui_evidence", EvidenceKind: "visual"}}},
		{"missing assessment", map[string]any{"description_assessments": []map[string]string{{"key": "ui_evidence", "status": "missing"}}, "risk_reasons": reasons, "coverage_complete": true}, false, nil},
		{"unverified coverage", map[string]any{"description_assessments": []map[string]string{{"key": "description", "status": "satisfied"}, {"key": "ui_evidence", "status": "missing"}}, "risk_reasons": reasons, "coverage_complete": false}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcomeRaw, err := json.Marshal(tt.outcome)
			require.NoError(t, err, "outcome should marshal")
			a := models.CodeReviewAssessment{ReviewScope: models.CodeReviewScopeFull, Status: models.CodeReviewAssessmentCompleted, CoverageComplete: true, InputManifest: raw, StructuredOutcome: outcomeRaw, RiskReasonDetails: reasonsRaw}
			got := baselineForPlanning(a, policy, files)
			require.NotNil(t, got, "baseline reader should return explicit coverage facts")
			require.Equal(t, tt.expectedCoverage, got.CoverageComplete, "incomplete structured coverage must fail closed")
			if tt.expectedCoverage {
				require.Equal(t, tt.expectedMissing, got.MissingRequirements, "missing requirement evidence kind must come from policy")
			}
		})
	}
}
