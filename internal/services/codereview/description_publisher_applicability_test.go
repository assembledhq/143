package codereview

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

const (
	publisherTestHead  = "abcdef1111111111111111111111111111111111"
	publisherTestAppID = int64(101)
)

func publisherTestSignal(t *testing.T, enabled bool, appID int64, payloadHead, render string) *models.CodeReviewRiskTierSignal {
	t.Helper()
	return publisherTestRunsSignal(t, enabled, publisherTestRun(t, 3, appID, payloadHead, render, "enforce"))
}

func publisherTestRunsSignal(t *testing.T, enabled bool, runs ...models.CodeReviewCheckRun) *models.CodeReviewRiskTierSignal {
	t.Helper()
	signal := models.ResolveCodeReviewRiskTierSignal(&models.CodeReviewRiskTierCheckPolicy{Enabled: enabled, AppID: publisherTestAppID}, publisherTestHead, runs)
	return &signal
}

func publisherTestRun(t *testing.T, id, appID int64, payloadHead, render, mode string) models.CodeReviewCheckRun {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"generation": map[string]any{
			"pr": 9, "head_sha": payloadHead, "base_sha": strings.Repeat("b", 40), "merge_base_sha": strings.Repeat("b", 40),
			"trunk_sha": strings.Repeat("c", 40), "index_id": nil, "policy_hash": strings.Repeat("d", 64), "mode": mode,
		},
		"tier": "high", "gate": "action_required", "override": false, "ui_render_change": render, "published_at": "2026-10-18T12:00:00Z",
	})
	require.NoError(t, err, "fixture payload should encode")
	return models.CodeReviewCheckRun{ID: id, Name: models.DefaultCodeReviewRiskTierCheckName, HeadSHA: publisherTestHead, AppID: appID, OutputText: models.CodeReviewRiskTierPayloadFence + "\n" + string(encoded) + "\n```"}
}

func TestApplicableDescriptionRequirementsPublisherResult(t *testing.T) {
	t.Parallel()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.DescriptionPolicy.Requirements = append(policy.DescriptionPolicy.Requirements, models.CodeReviewDescriptionRequirement{
		Key: "styles", Title: "Style review", Required: true, EvidenceKind: models.CodeReviewDescriptionEvidenceKindGeneral, Prompt: "Explain the styling change.",
		AppliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"**/*.tsx"}},
	})
	uiFiles := []PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	backendFiles := []PullRequestFile{{Filename: "internal/api/review.go", Additions: 2}}
	tests := []struct {
		name             string
		files            []PullRequestFile
		signal           func(t *testing.T) *models.CodeReviewRiskTierSignal
		expected         []string
		requiredByResult bool
	}{
		{name: "no signal keeps today's paths on UI files", files: uiFiles, signal: func(*testing.T) *models.CodeReviewRiskTierSignal { return nil }, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "no signal keeps today's paths on backend files", files: backendFiles, signal: func(*testing.T) *models.CodeReviewRiskTierSignal { return nil }, expected: []string{"description"}},
		{name: "disabled setting ignores an authoritative-looking false", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, false, publisherTestAppID, publisherTestHead, "false")
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "false makes ui evidence not applicable on UI files", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "false")
		}, expected: []string{"description", "styles"}},
		{name: "true makes ui evidence applicable on backend files", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "true")
		}, expected: []string{"description", "ui_evidence"}, requiredByResult: true},
		{name: "unknown falls back to paths on UI files", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "unknown")
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "unknown falls back to paths on backend files", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "unknown")
		}, expected: []string{"description"}},
		{name: "stale-head false falls back to paths", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, strings.Repeat("e", 40), "false")
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "wrong-app false falls back to paths", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, 15368, publisherTestHead, "false")
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "wrong-app true falls back to paths", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, 15368, publisherTestHead, "true")
		}, expected: []string{"description"}},
		{name: "shadow false falls back to paths", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "false", "shadow"))
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "older false and newer true requires ui evidence", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "false", "enforce"), publisherTestRun(t, 4, publisherTestAppID, publisherTestHead, "true", "enforce"))
		}, expected: []string{"description", "ui_evidence"}, requiredByResult: true},
		{name: "older true and newer false requires ui evidence", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "true", "enforce"), publisherTestRun(t, 4, publisherTestAppID, publisherTestHead, "false", "enforce"))
		}, expected: []string{"description", "ui_evidence"}, requiredByResult: true},
		{name: "unknown beside false falls back to paths", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "unknown", "enforce"), publisherTestRun(t, 4, publisherTestAppID, publisherTestHead, "false", "enforce"))
		}, expected: []string{"description", "ui_evidence", "styles"}},
		{name: "true beside a shadow run still requires ui evidence", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "true", "enforce"), publisherTestRun(t, 4, publisherTestAppID, publisherTestHead, "false", "shadow"))
		}, expected: []string{"description", "ui_evidence"}, requiredByResult: true},
		{name: "false beside a shadow run falls back to paths", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestRunsSignal(t, true, publisherTestRun(t, 3, publisherTestAppID, publisherTestHead, "false", "enforce"), publisherTestRun(t, 4, publisherTestAppID, publisherTestHead, "true", "shadow"))
		}, expected: []string{"description", "ui_evidence", "styles"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			signal := tt.signal(t)
			requirements := ApplicableDescriptionRequirements(policy, tt.files, signal)
			keys := make([]string, 0, len(requirements))
			requiredByResult := false
			for _, r := range requirements {
				keys = append(keys, r.Key)
				if r.Key == "styles" {
					require.False(t, DescriptionRequirementRequiredByPublisher(r, signal), "a paths requirement is never decided by the publisher")
				}
				if r.Key == "ui_evidence" {
					requiredByResult = DescriptionRequirementRequiredByPublisher(r, signal)
				}
			}
			require.Equal(t, tt.expected, keys, "ui_evidence should follow the authoritative render change and every other requirement should be unchanged")
			require.Equal(t, tt.requiredByResult, requiredByResult, "only an authoritative true should force ui_evidence")
		})
	}
}

func TestBaselineForPlanningUsesRecordedPublisherResult(t *testing.T) {
	t.Parallel()
	manifest, err := BuildReviewInputManifest(reviewTestCapture())
	require.NoError(t, err, "baseline fixture should build")
	raw, err := json.Marshal(manifest)
	require.NoError(t, err, "baseline manifest should marshal")
	policy := models.DefaultCodeReviewPolicyConfig()
	uiFiles := []PullRequestFile{{Filename: "frontend/src/app/page.tsx", Additions: 2}}
	backendFiles := []PullRequestFile{{Filename: "internal/api/review.go", Additions: 2}}
	reasons := []models.CodeReviewRiskReason{{Code: models.CodeReviewRiskReasonDescriptionFailed}}
	reasonsRaw, err := json.Marshal(reasons)
	require.NoError(t, err, "risk reasons should marshal")
	tests := []struct {
		name             string
		files            []PullRequestFile
		signal           func(t *testing.T) *models.CodeReviewRiskTierSignal
		assessments      []map[string]string
		expectedCoverage bool
		expectedMissing  []RecheckMissingRequirement
	}{
		{name: "recorded false excludes ui evidence on UI files", files: uiFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "false")
		}, assessments: []map[string]string{{"key": "description", "status": "missing"}}, expectedCoverage: true, expectedMissing: []RecheckMissingRequirement{{ID: "description", EvidenceKind: "general"}}},
		{name: "recorded true counts a not-applicable ui assessment as missing", files: backendFiles, signal: func(t *testing.T) *models.CodeReviewRiskTierSignal {
			return publisherTestSignal(t, true, publisherTestAppID, publisherTestHead, "true")
		}, assessments: []map[string]string{{"key": "description", "status": "satisfied"}, {"key": "ui_evidence", "status": "not_applicable"}}, expectedCoverage: true, expectedMissing: []RecheckMissingRequirement{{ID: "ui_evidence", EvidenceKind: "visual"}}},
		{name: "no recorded signal keeps today's not-applicable pass", files: uiFiles, signal: func(*testing.T) *models.CodeReviewRiskTierSignal { return nil }, assessments: []map[string]string{{"key": "description", "status": "missing"}, {"key": "ui_evidence", "status": "not_applicable"}}, expectedCoverage: true, expectedMissing: []RecheckMissingRequirement{{ID: "description", EvidenceKind: "general"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome := map[string]any{"description_assessments": tt.assessments, "risk_reasons": reasons, "coverage_complete": true}
			if signal := tt.signal(t); signal != nil {
				outcome["risk_tier_signal"] = signal
			}
			outcomeRaw, err := json.Marshal(outcome)
			require.NoError(t, err, "outcome should marshal")
			a := models.CodeReviewAssessment{ReviewScope: models.CodeReviewScopeFull, Status: models.CodeReviewAssessmentCompleted, CoverageComplete: true, InputManifest: raw, StructuredOutcome: outcomeRaw, RiskReasonDetails: reasonsRaw}
			got := baselineForPlanning(a, policy, tt.files)
			require.NotNil(t, got, "baseline reader should return explicit coverage facts")
			require.Equal(t, tt.expectedCoverage, got.CoverageComplete, "the recorded publisher result should reproduce the assessed requirement set")
			require.Equal(t, tt.expectedMissing, got.MissingRequirements, "recheck planning should see the requirements the review actually held")
		})
	}
}
