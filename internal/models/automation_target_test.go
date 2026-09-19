package models

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAutomationSessionContinuity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		continuity    AutomationSessionContinuity
		triggers      []AutomationGitHubEvent
		publishPolicy AutomationPublishPolicy
		wantField     string
	}{
		{name: "per_run needs nothing", continuity: AutomationSessionContinuityPerRun},
		{name: "empty defaults to per_run", continuity: "", publishPolicy: AutomationPublishPolicyPullRequest},
		{
			name:          "per_target with trigger and no publication",
			continuity:    AutomationSessionContinuityPerTarget,
			triggers:      []AutomationGitHubEvent{AutomationGitHubEventPullRequestUpdated},
			publishPolicy: AutomationPublishPolicyNone,
		},
		{
			name:          "per_target without triggers",
			continuity:    AutomationSessionContinuityPerTarget,
			publishPolicy: AutomationPublishPolicyNone,
			wantField:     "github_event_triggers",
		},
		{
			name:          "per_target with pull request publication",
			continuity:    AutomationSessionContinuityPerTarget,
			triggers:      []AutomationGitHubEvent{AutomationGitHubEventPullRequestOpened},
			publishPolicy: AutomationPublishPolicyPullRequest,
			wantField:     "publish_policy",
		},
		{
			name:          "per_target with defaulted publication",
			continuity:    AutomationSessionContinuityPerTarget,
			triggers:      []AutomationGitHubEvent{AutomationGitHubEventPullRequestOpened},
			publishPolicy: "",
			wantField:     "publish_policy",
		},
		{name: "unknown value", continuity: "per_org", wantField: "session_continuity"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAutomationSessionContinuity(tt.continuity, tt.triggers, tt.publishPolicy)
			if tt.wantField == "" {
				require.NoError(t, err, "continuity settings should validate")
				return
			}
			var continuityErr *AutomationSessionContinuityError
			require.ErrorAs(t, err, &continuityErr, "rejection should carry the offending field")
			require.Equal(t, tt.wantField, continuityErr.Field, "rejection should name the offending field")
			require.Equal(t, continuityErr.Message, err.Error(), "error text should be the rejection message")
		})
	}
}

func TestAutomationSessionContinuityOrDefault(t *testing.T) {
	t.Parallel()

	require.Equal(t, AutomationSessionContinuityPerRun, AutomationSessionContinuity("").OrDefault(), "empty continuity should default to per_run")
	require.Equal(t, AutomationSessionContinuityPerTarget, AutomationSessionContinuityPerTarget.OrDefault(), "explicit continuity should be preserved")
}

func TestAutomationRunOutcomeReasonRunStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason AutomationRunOutcomeReason
		status AutomationRunStatus
	}{
		{AutomationRunOutcomeTurnCompleted, AutomationRunStatusCompleted},
		{AutomationRunOutcomeHeadLookupDegraded, AutomationRunStatusCompleted},
		{AutomationRunOutcomeAgentFailed, AutomationRunStatusFailed},
		{AutomationRunOutcomeCancelled, AutomationRunStatusFailed},
		{AutomationRunOutcomeAwaitingInput, AutomationRunStatusFailed},
		{AutomationRunOutcomeRetriesExhausted, AutomationRunStatusFailed},
		{AutomationRunOutcomeStaleHead, AutomationRunStatusSkipped},
		{AutomationRunOutcomeDuplicateHead, AutomationRunStatusSkipped},
		{AutomationRunOutcomeSuperseded, AutomationRunStatusSkipped},
		{AutomationRunOutcomeWaitTimeout, AutomationRunStatusFailed},
		{AutomationRunOutcomeWaitOverflow, AutomationRunStatusFailed},
		{AutomationRunOutcomePRClosed, AutomationRunStatusSkipped},
		{AutomationRunOutcomeRepositoryUnavailable, AutomationRunStatusFailed},
	}
	for _, tt := range tests {
		t.Run(string(tt.reason), func(t *testing.T) {
			t.Parallel()
			require.NoError(t, tt.reason.Validate(), "every mapped outcome reason should be a valid enum value")
			require.Equal(t, tt.status, tt.reason.RunStatus(), "outcome reason should map to the design's run status")
		})
	}
}

func TestAutomationContinuityEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		valid   func() error
		invalid func() error
	}{
		{"target kind", AutomationTargetKindGitHubPullRequest.Validate, AutomationTargetKind("jira_issue").Validate},
		{"lifecycle state", AutomationTargetLifecycleMerged.Validate, AutomationTargetLifecycleState("draft").Validate},
		{"generation status", AutomationTargetSessionStatusRetired.Validate, AutomationTargetSessionStatus("paused").Validate},
		{"retired reason", AutomationTargetRetiredContinuityDisabled.Validate, AutomationTargetRetiredReason("bored").Validate},
		{"head resolution", AutomationRunHeadUnresolved.Validate, AutomationRunHeadResolution("guessed").Validate},
		{"continuation mode", AutomationRunContinuationReconstructed.Validate, AutomationRunContinuationMode("warm").Validate},
		{"continuation reason", AutomationRunContinuationReasonSandboxDestroyed.Validate, AutomationRunContinuationReason("moon").Validate},
		{"dispatch state", AutomationRunDispatchDone.Validate, AutomationRunDispatchState("queued").Validate},
		{"wait reason", AutomationRunWaitTargetBusy.Validate, AutomationRunWaitReason("weather").Validate},
		{"result outcome", AutomationRunResultCancelled.Validate, AutomationRunResultOutcome("superseded").Validate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, tt.valid(), "known value should validate")
			require.Error(t, tt.invalid(), "unknown value should be rejected")
		})
	}
}

func TestAutomationRunResultOutcomeReasonMapping(t *testing.T) {
	t.Parallel()

	require.Equal(t, AutomationRunOutcomeAwaitingInput, AutomationRunResultAwaitingInput.OutcomeReason(), "marker outcome should map onto the run outcome reason by value")
	require.Equal(t, AutomationRunContinuationReasonTurnLimit, ContinuationReasonForRetirement(AutomationTargetRetiredTurnLimit), "retire reason should map onto the continuation reason by value")
	require.NoError(t, AutomationRunResultTurnCompleted.OutcomeReason().Validate(), "mapped outcome reason should be a valid enum value")
	for _, reason := range []AutomationTargetRetiredReason{
		AutomationTargetRetiredSessionUnavailable, AutomationTargetRetiredNotResumable,
		AutomationTargetRetiredAgentConfigChanged, AutomationTargetRetiredIdentityChanged,
		AutomationTargetRetiredBaseRetargeted, AutomationTargetRetiredTurnLimit,
		AutomationTargetRetiredSnapshotTooLarge, AutomationTargetRetiredUnsupportedWorkspace,
		AutomationTargetRetiredAwaitingInput,
	} {
		require.NoError(t, ContinuationReasonForRetirement(reason).Validate(), "compatibility retire reason %q should be a valid continuation reason", reason)
	}
}

func TestBuildConfigSnapshot_SessionContinuity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		continuity AutomationSessionContinuity
		want       string
	}{
		{name: "explicit per_target", continuity: AutomationSessionContinuityPerTarget, want: "per_target"},
		{name: "defaults to per_run", continuity: "", want: "per_run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := Automation{BaseBranch: "main", SessionContinuity: tt.continuity}
			raw, err := a.BuildConfigSnapshot()
			require.NoError(t, err, "config snapshot should marshal")
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(raw, &decoded), "config snapshot should be valid JSON")
			require.Equal(t, tt.want, decoded["session_continuity"], "config snapshot should capture the continuity mode for audit")
		})
	}
}

func TestAutomationSessionContinuityErrorUnwrap(t *testing.T) {
	t.Parallel()

	err := ValidateAutomationSessionContinuity(AutomationSessionContinuityPerTarget, nil, AutomationPublishPolicyNone)
	var target *AutomationSessionContinuityError
	require.True(t, errors.As(err, &target), "validation error should be an AutomationSessionContinuityError")
}
