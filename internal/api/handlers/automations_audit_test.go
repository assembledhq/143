package handlers

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestAutomationAuditSnapshot_Interval(t *testing.T) {
	t.Parallel()

	v, u := 3, "days"
	a := &models.Automation{
		Name:          "Refresh caches",
		ScheduleType:  models.AutomationScheduleInterval,
		IntervalValue: &v,
		IntervalUnit:  &u,
		Timezone:      "UTC",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, "Refresh caches", snap["name"])
	require.Equal(t, models.AutomationScheduleInterval, snap["schedule_type"])
	require.Equal(t, 3, snap["interval_value"])
	require.Equal(t, "days", snap["interval_unit"])
	_, hasCron := snap["cron_expression"]
	require.False(t, hasCron, "interval snapshot must not include cron fields")
}

func TestAutomationAuditSnapshot_Cron(t *testing.T) {
	t.Parallel()

	expr := "0 9 * * 1"
	a := &models.Automation{
		Name:           "Monday briefing",
		ScheduleType:   models.AutomationScheduleCron,
		CronExpression: &expr,
		Timezone:       "America/Los_Angeles",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, "0 9 * * 1", snap["cron_expression"])
	require.Equal(t, "America/Los_Angeles", snap["timezone"])
	_, hasInterval := snap["interval_value"]
	require.False(t, hasInterval, "cron snapshot must not include interval fields")
}

func TestAutomationAuditDiff_OnlyChangedFields(t *testing.T) {
	t.Parallel()

	oldV, newV := 1, 7
	oldU, newU := "days", "days"
	old := models.Automation{
		Name: "a", Goal: "g", ExecutionMode: "sequential", MaxConcurrent: 1,
		BaseBranch: "main", ScheduleType: models.AutomationScheduleInterval,
		IntervalValue: &oldV, IntervalUnit: &oldU, Timezone: "UTC", Priority: 50,
	}
	new_ := old
	new_.Name = "b"
	new_.IntervalValue = &newV
	new_.IntervalUnit = &newU // unchanged
	new_.Priority = 75

	changes := automationAuditDiff(&old, &new_)
	require.Len(t, changes, 3, "only name, interval_value, priority should change")
	require.Contains(t, changes, "name")
	require.Contains(t, changes, "interval_value")
	require.Contains(t, changes, "priority")

	nameChange := changes["name"].(map[string]any)
	require.Equal(t, "a", nameChange["before"])
	require.Equal(t, "b", nameChange["after"])

	intervalChange := changes["interval_value"].(map[string]any)
	require.Equal(t, 1, intervalChange["before"])
	require.Equal(t, 7, intervalChange["after"])
}

func TestAutomationAuditDiff_NoChanges(t *testing.T) {
	t.Parallel()

	a := models.Automation{
		Name: "a", Goal: "g", ExecutionMode: "sequential", MaxConcurrent: 1,
		BaseBranch: "main", ScheduleType: models.AutomationScheduleInterval,
		Timezone: "UTC", Priority: 50,
	}
	changes := automationAuditDiff(&a, &a)
	require.Empty(t, changes, "identical automations must yield an empty diff")
}

func TestAutomationAuditDiff_RepositoryIDTransitions(t *testing.T) {
	t.Parallel()

	repoA := uuid.New()
	repoB := uuid.New()

	t.Run("nil to set", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: nil}
		new_ := models.Automation{RepositoryID: &repoA}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "repository_id")
	})

	t.Run("set to different", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: &repoA}
		new_ := models.Automation{RepositoryID: &repoB}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "repository_id")
	})

	t.Run("same value", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{RepositoryID: &repoA}
		new_ := models.Automation{RepositoryID: &repoA}
		changes := automationAuditDiff(&old, &new_)
		require.NotContains(t, changes, "repository_id")
	})
}

func TestMarshalAuditDetails(t *testing.T) {
	t.Parallel()

	t.Run("empty map returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, marshalAuditDetails(map[string]any{}))
		require.Nil(t, marshalAuditDetails(nil))
	})

	t.Run("valid payload round-trips", func(t *testing.T) {
		t.Parallel()
		got := marshalAuditDetails(map[string]any{"name": "x", "count": 3})
		require.NotNil(t, got)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(got, &decoded))
		require.Equal(t, "x", decoded["name"])
		require.Equal(t, float64(3), decoded["count"])
	})
}
