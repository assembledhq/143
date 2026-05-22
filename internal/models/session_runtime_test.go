package models

import "testing"

import "github.com/stretchr/testify/require"

func TestRuntimeProgressStrength_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, RuntimeProgressStrengthNone.Validate(), "empty runtime progress strength should be valid")
	require.NoError(t, RuntimeProgressStrengthWeak.Validate(), "weak runtime progress strength should be valid")
	require.NoError(t, RuntimeProgressStrengthStrong.Validate(), "strong runtime progress strength should be valid")
	require.Error(t, RuntimeProgressStrength("bad").Validate(), "unknown runtime progress strength should be rejected")
}

func TestRuntimeProgressType_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, RuntimeProgressTypeNone.Validate(), "empty runtime progress type should be valid")
	require.NoError(t, RuntimeProgressTypeToolResult.Validate(), "tool result runtime progress type should be valid")
	require.NoError(t, RuntimeProgressTypeCheckpoint.Validate(), "checkpoint runtime progress type should be valid")
	require.Error(t, RuntimeProgressType("bad").Validate(), "unknown runtime progress type should be rejected")
}

func TestRuntimeStopReason_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, RuntimeStopReasonNone.Validate(), "empty runtime stop reason should be valid")
	require.NoError(t, RuntimeStopReasonSoftBudget.Validate(), "soft budget runtime stop reason should be valid")
	require.NoError(t, RuntimeStopReasonNoProgress.Validate(), "no progress runtime stop reason should be valid")
	require.Error(t, RuntimeStopReason("bad").Validate(), "unknown runtime stop reason should be rejected")
}

func TestCheckpointCapability_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, CheckpointCapabilityNone.Validate(), "empty checkpoint capability should be valid")
	require.NoError(t, CheckpointCapabilityFullResume.Validate(), "full resume checkpoint capability should be valid")
	require.NoError(t, CheckpointCapabilityFilesystemOnly.Validate(), "filesystem-only checkpoint capability should be valid")
	require.Error(t, CheckpointCapability("bad").Validate(), "unknown checkpoint capability should be rejected")
}

func TestCheckpointKind_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, CheckpointKindNone.Validate(), "empty checkpoint kind should be valid")
	require.NoError(t, CheckpointKindBootstrap.Validate(), "bootstrap checkpoint kind should be valid")
	require.NoError(t, CheckpointKindTurnComplete.Validate(), "turn-complete checkpoint kind should be valid")
	require.NoError(t, CheckpointKindGracefulStop.Validate(), "graceful-stop checkpoint kind should be valid")
	require.Error(t, CheckpointKind("bad").Validate(), "unknown checkpoint kind should be rejected")
}

func TestRecoveryState_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, RecoveryStateNone.Validate(), "empty recovery state should be valid")
	require.NoError(t, RecoveryStateQueued.Validate(), "queued recovery state should be valid")
	require.NoError(t, RecoveryStateRecovering.Validate(), "recovering recovery state should be valid")
	require.Error(t, RecoveryState("bad").Validate(), "unknown recovery state should be rejected")
}
