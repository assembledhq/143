package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPRFeedbackEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "monitoring", validate: func() error { return PRFeedbackMonitoringEnabled.Validate() }},
		{name: "invalid monitoring", validate: func() error { return PRFeedbackMonitoring("bad").Validate() }, expectErr: true},
		{name: "human mode", validate: func() error { return PRFeedbackHumanModeMentions.Validate() }},
		{name: "invalid human mode", validate: func() error { return PRFeedbackHumanMode("bad").Validate() }, expectErr: true},
		{name: "bot mode", validate: func() error { return PRFeedbackBotModeAllowlist.Validate() }},
		{name: "invalid bot mode", validate: func() error { return PRFeedbackBotMode("bad").Validate() }, expectErr: true},
		{name: "surface", validate: func() error { return PRFeedbackSurfaceReviewComment.Validate() }},
		{name: "invalid surface", validate: func() error { return PRFeedbackSurface("bad").Validate() }, expectErr: true},
		{name: "intent", validate: func() error { return PRFeedbackIntentMixed.Validate() }},
		{name: "invalid intent", validate: func() error { return PRFeedbackIntent("bad").Validate() }, expectErr: true},
		{name: "item status", validate: func() error { return PRFeedbackItemStatusResponded.Validate() }},
		{name: "invalid item status", validate: func() error { return PRFeedbackItemStatus("bad").Validate() }, expectErr: true},
		{name: "batch status", validate: func() error { return PRFeedbackBatchStatusRunning.Validate() }},
		{name: "invalid batch status", validate: func() error { return PRFeedbackBatchStatus("bad").Validate() }, expectErr: true},
		{name: "source kind", validate: func() error { return PRFeedbackBatchSourceBotOnly.Validate() }},
		{name: "invalid source kind", validate: func() error { return PRFeedbackBatchSourceKind("bad").Validate() }, expectErr: true},
		{name: "bot eligibility", validate: func() error { return PRFeedbackBotEligibilityInstalledApp.Validate() }},
		{name: "invalid bot eligibility", validate: func() error { return PRFeedbackBotEligibilitySource("bad").Validate() }, expectErr: true},
		{name: "author type", validate: func() error { return PRFeedbackAuthorTypeBot.Validate() }},
		{name: "invalid author type", validate: func() error { return PRFeedbackAuthorType("bad").Validate() }, expectErr: true},
		{name: "workspace mode", validate: func() error { return PRFeedbackWorkspaceModePRHeadReconstruction.Validate() }},
		{name: "invalid workspace mode", validate: func() error { return PRFeedbackWorkspaceMode("bad").Validate() }, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid feedback enum should be rejected")
				return
			}
			require.NoError(t, err, "valid feedback enum should be accepted")
		})
	}
}

func TestNullableCycleLimitJSON(t *testing.T) {
	t.Parallel()

	three := 3
	tests := []struct {
		name      string
		raw       string
		expected  NullableCycleLimit
		effective *int
		expectErr bool
	}{
		{name: "null is unlimited", raw: "null", expected: NullableCycleLimit{Set: true}, effective: nil},
		{name: "zero disables", raw: "0", expected: NullableCycleLimit{Set: true, Value: intPtr(0)}, effective: intPtr(0)},
		{name: "finite limit", raw: "12", expected: NullableCycleLimit{Set: true, Value: intPtr(12)}, effective: intPtr(12)},
		{name: "negative rejected", raw: "-1", expectErr: true},
		{name: "over maximum rejected", raw: "101", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got NullableCycleLimit
			err := json.Unmarshal([]byte(tt.raw), &got)
			if tt.expectErr {
				require.Error(t, err, "invalid cycle limit should be rejected")
				return
			}
			require.NoError(t, err, "valid cycle limit should decode")
			require.Equal(t, tt.expected, got, "cycle limit should preserve its JSON state")
			require.Equal(t, tt.effective, got.Effective(), "cycle limit should resolve expected effective value")
		})
	}

	unset := NullableCycleLimit{}
	require.Equal(t, &three, unset.Effective(), "absent cycle limit should default to three")
}

func TestPRFeedbackTriageResultValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		result    PRFeedbackTriageResult
		expectErr bool
	}{
		{name: "change request", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentChangeRequest, RequiresAgent: true, RequiresCodeChange: true, Reason: "code must change"}},
		{name: "question without code", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentQuestion, RequiresAgent: true, Reason: "answer from repository context"}},
		{name: "acknowledgement", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentAcknowledgement, Reason: "no action"}},
		{name: "unknown intent", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentUnknown, Reason: "not classified"}, expectErr: true},
		{name: "missing reason", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentQuestion, RequiresAgent: true}, expectErr: true},
		{name: "acknowledgement requiring agent", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentAcknowledgement, RequiresAgent: true, Reason: "invalid"}, expectErr: true},
		{name: "code without agent", result: PRFeedbackTriageResult{Intent: PRFeedbackIntentChangeRequest, RequiresCodeChange: true, Reason: "invalid"}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.result.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid triage result should be rejected")
				return
			}
			require.NoError(t, err, "valid triage result should be accepted")
		})
	}
}

func intPtr(value int) *int { return &value }
