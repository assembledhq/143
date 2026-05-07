package models

import (
	"encoding/json"
	"testing"
	"time"

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

func TestAutomationIdentityScopeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scope     AutomationIdentityScope
		expectErr bool
	}{
		{name: "org is valid", scope: AutomationIdentityScopeOrg},
		{name: "personal is valid", scope: AutomationIdentityScopePersonal},
		{name: "empty defaults valid", scope: ""},
		{name: "invalid", scope: "team", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.scope.Validate()
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateCronExpression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expr      string
		expectErr bool
	}{
		{name: "5-field daily", expr: "0 9 * * *"},
		{name: "5-field weekly", expr: "0 9 * * 1"},
		{name: "6-field with seconds", expr: "0 0 9 * * *"},
		{name: "alias", expr: "@daily"},
		{name: "empty", expr: "", expectErr: true},
		{name: "garbage", expr: "every monday", expectErr: true},
		{name: "too few fields", expr: "0 9 *", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCronExpression(tt.expr)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateIntervalRunAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		{name: "valid five minute boundary", input: "09:35"},
		{name: "invalid format", input: "9:35", expectErr: true},
		{name: "invalid parse with correct length", input: "ab:cd", expectErr: true},
		{name: "invalid minute step", input: "09:37", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateIntervalRunAt(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNextCronRunTime(t *testing.T) {
	t.Parallel()

	// 9am daily, evaluated from 8am UTC → next fire is 9am UTC same day.
	from := time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)
	next, err := NextCronRunTime("0 9 * * *", "UTC", from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), next)

	// Same cron in America/New_York: 9am ET on 2026-04-17 = 13:00 UTC (EDT).
	next, err = NextCronRunTime("0 9 * * *", "America/New_York", from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC), next)

	// Unknown timezone.
	_, err = NextCronRunTime("0 9 * * *", "Mars/Olympus", from)
	require.Error(t, err)

	// Malformed expression.
	_, err = NextCronRunTime("not a cron", "UTC", from)
	require.Error(t, err)
}

func TestComputeNextRunAt(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)

	iv := 6
	iu := "hours"
	interval := Automation{
		ScheduleType:  AutomationScheduleInterval,
		IntervalValue: &iv,
		IntervalUnit:  &iu,
	}
	got, err := interval.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, from.Add(6*time.Hour), got)

	runAt := "11:15"
	interval.IntervalRunAt = &runAt
	got, err = interval.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 14, 15, 0, 0, time.UTC), got)

	invalidRunAt := "ab:cd"
	interval.IntervalRunAt = &invalidRunAt
	_, err = interval.ComputeNextRunAt(from)
	require.Error(t, err)

	// Interval with missing companion fields is rejected (corrupt row).
	bad := Automation{ScheduleType: AutomationScheduleInterval}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	expr := "0 9 * * *"
	cron := Automation{
		ScheduleType:   AutomationScheduleCron,
		CronExpression: &expr,
		Timezone:       "UTC",
	}
	got, err = cron.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), got)

	// Cron with missing expression is rejected.
	bad = Automation{ScheduleType: AutomationScheduleCron}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	// Unknown schedule kind is rejected.
	bad = Automation{ScheduleType: "event"}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	// Empty timezone on a cron schedule defaults to UTC so legacy rows
	// imported without an explicit zone still fire correctly.
	cronNoTz := Automation{
		ScheduleType:   AutomationScheduleCron,
		CronExpression: &expr,
	}
	got, err = cronNoTz.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), got)
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
	reasoning := ReasoningEffortXHigh
	a := Automation{
		AgentType:       &agent,
		ModelOverride:   &model,
		ReasoningEffort: &reasoning,
		Scope:           &scope,
		IdentityScope:   AutomationIdentityScopePersonal,
		BaseBranch:      "main",
	}

	raw, err := a.BuildConfigSnapshot()
	require.NoError(t, err)
	require.NotEmpty(t, raw)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "codex", decoded["agent_type"])
	require.Equal(t, "opus-4-7", decoded["model_override"])
	require.Equal(t, "xhigh", decoded["reasoning_effort"])
	require.Equal(t, "src/", decoded["scope"])
	require.Equal(t, string(AutomationIdentityScopePersonal), decoded["identity_scope"])
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
	require.Nil(t, decoded["reasoning_effort"])
	require.Nil(t, decoded["scope"])
	require.Equal(t, string(AutomationIdentityScopeOrg), decoded["identity_scope"])
	require.Equal(t, "develop", decoded["base_branch"])
}
