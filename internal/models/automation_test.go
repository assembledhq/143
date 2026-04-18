package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAutomationScheduleType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		{name: "interval is valid", input: AutomationScheduleInterval},
		{name: "cron is valid", input: AutomationScheduleCron},
		{name: "empty is invalid", input: "", expectErr: true},
		{name: "garbage is invalid", input: "every-5-minutes", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAutomationScheduleType(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateAutomationScheduleSupported(t *testing.T) {
	t.Parallel()

	// interval is always supported.
	require.NoError(t, ValidateAutomationScheduleSupported(AutomationScheduleInterval))

	// Cron passes type validation but must be gated by AutomationCronSupported
	// until the cron parser lands. If this test starts failing because
	// AutomationCronSupported flipped to true, make sure NextRunTime handles
	// cron and remove the cron-specific branch in the function above.
	if AutomationCronSupported {
		require.NoError(t, ValidateAutomationScheduleSupported(AutomationScheduleCron))
	} else {
		err := ValidateAutomationScheduleSupported(AutomationScheduleCron)
		require.Error(t, err, "cron must be rejected while AutomationCronSupported is false")
		require.Contains(t, err.Error(), "cron")
	}

	// Unknown type is rejected with the same error ValidateAutomationScheduleType returns.
	require.Error(t, ValidateAutomationScheduleSupported("every-friday"))
}

func TestValidateAutomationRunStatus(t *testing.T) {
	t.Parallel()

	valid := []string{
		AutomationRunStatusPending,
		AutomationRunStatusRunning,
		AutomationRunStatusCompleted,
		AutomationRunStatusCompletedNoop,
		AutomationRunStatusFailed,
		AutomationRunStatusSkipped,
	}
	for _, s := range valid {
		t.Run("valid_"+s, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, ValidateAutomationRunStatus(s))
		})
	}

	invalid := []string{"", "queued", "canceled", "PENDING"}
	for _, s := range invalid {
		t.Run("invalid_"+s, func(t *testing.T) {
			t.Parallel()
			require.Error(t, ValidateAutomationRunStatus(s))
		})
	}
}

func TestBuildConfigSnapshot(t *testing.T) {
	t.Parallel()

	agent := "codex"
	model := "opus-4-7"
	scope := "src/"
	a := Automation{
		AgentType:     &agent,
		ModelOverride: &model,
		Scope:         &scope,
		BaseBranch:    "main",
	}

	raw, err := a.BuildConfigSnapshot()
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "codex", decoded["agent_type"])
	require.Equal(t, "opus-4-7", decoded["model_override"])
	require.Equal(t, "src/", decoded["scope"])
	require.Equal(t, "main", decoded["base_branch"])
}

func TestBuildConfigSnapshot_NilOptionalFields(t *testing.T) {
	t.Parallel()

	a := Automation{BaseBranch: "develop"}

	raw, err := a.BuildConfigSnapshot()
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Nil(t, decoded["agent_type"])
	require.Nil(t, decoded["model_override"])
	require.Nil(t, decoded["scope"])
	require.Equal(t, "develop", decoded["base_branch"])
}
