package handlers

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestAutomationAuditSnapshot_Interval(t *testing.T) {
	t.Parallel()

	v, u := 3, "days"
	runAt := "09:35"
	a := &models.Automation{
		Name:          "Refresh caches",
		IdentityScope: models.AutomationIdentityScopePersonal,
		ScheduleType:  models.AutomationScheduleInterval,
		IntervalValue: &v,
		IntervalUnit:  &u,
		IntervalRunAt: &runAt,
		Timezone:      "UTC",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, "Refresh caches", snap["name"])
	require.Equal(t, models.AutomationIdentityScopePersonal, snap["identity_scope"])
	require.Equal(t, models.AutomationScheduleInterval, snap["schedule_type"])
	require.Equal(t, 3, snap["interval_value"])
	require.Equal(t, "days", snap["interval_unit"])
	require.Equal(t, "09:35", snap["interval_run_at"])
	_, hasCron := snap["cron_expression"]
	require.False(t, hasCron, "interval snapshot must not include cron fields")
}

func TestAutomationAuditSnapshot_Cron(t *testing.T) {
	t.Parallel()

	expr := "0 9 * * 1"
	a := &models.Automation{
		Name:           "Monday briefing",
		IdentityScope:  models.AutomationIdentityScopeOrg,
		ScheduleType:   models.AutomationScheduleCron,
		CronExpression: &expr,
		Timezone:       "America/Los_Angeles",
	}
	snap := automationAuditSnapshot(a)
	require.Equal(t, models.AutomationIdentityScopeOrg, snap["identity_scope"])
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
		BaseBranch: "main", IdentityScope: models.AutomationIdentityScopeOrg, ScheduleType: models.AutomationScheduleInterval,
		IntervalValue: &oldV, IntervalUnit: &oldU, Timezone: "UTC", Priority: 50,
	}
	new_ := old
	new_.Name = "b"
	new_.IdentityScope = models.AutomationIdentityScopePersonal
	new_.IntervalValue = &newV
	new_.IntervalUnit = &newU // unchanged
	new_.Priority = 75

	changes := automationAuditDiff(&old, &new_)
	require.Len(t, changes, 4, "only name, identity_scope, interval_value, and priority should change")
	require.Contains(t, changes, "name")
	require.Contains(t, changes, "identity_scope")
	require.Contains(t, changes, "interval_value")
	require.Contains(t, changes, "priority")

	nameChange := changes["name"].(map[string]any)
	require.Equal(t, "a", nameChange["before"])
	require.Equal(t, "b", nameChange["after"])

	scopeChange := changes["identity_scope"].(map[string]any)
	require.Equal(t, models.AutomationIdentityScopeOrg, scopeChange["before"])
	require.Equal(t, models.AutomationIdentityScopePersonal, scopeChange["after"])

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

// TestAutomationAuditDiff_OptionalFieldsTriState pins the nil-vs-empty
// distinction: clearing an optional string (nil → "" or vice versa) must
// show up as a change so the audit timeline doesn't silently collapse the
// transition. Earlier, derefString-based diff treated nil and "" identically.
func TestAutomationAuditDiff_OptionalFieldsTriState(t *testing.T) {
	t.Parallel()

	t.Run("nil to empty string counts as change", func(t *testing.T) {
		t.Parallel()
		empty := ""
		old := models.Automation{Scope: nil}
		new_ := models.Automation{Scope: &empty}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "scope")
		scope := changes["scope"].(map[string]any)
		require.Nil(t, scope["before"])
		require.Equal(t, "", scope["after"])
	})

	t.Run("set to nil counts as change", func(t *testing.T) {
		t.Parallel()
		expr := "0 9 * * 1"
		old := models.Automation{CronExpression: &expr}
		new_ := models.Automation{CronExpression: nil}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "cron_expression")
		c := changes["cron_expression"].(map[string]any)
		require.Equal(t, "0 9 * * 1", c["before"])
		require.Nil(t, c["after"])
	})

	t.Run("nil interval to zero counts as change", func(t *testing.T) {
		t.Parallel()
		zero := 0
		old := models.Automation{IntervalValue: nil}
		new_ := models.Automation{IntervalValue: &zero}
		changes := automationAuditDiff(&old, &new_)
		require.Contains(t, changes, "interval_value")
		c := changes["interval_value"].(map[string]any)
		require.Nil(t, c["before"])
		require.Equal(t, 0, c["after"])
	})

	t.Run("both nil does not surface a change", func(t *testing.T) {
		t.Parallel()
		old := models.Automation{Scope: nil}
		new_ := models.Automation{Scope: nil}
		changes := automationAuditDiff(&old, &new_)
		require.NotContains(t, changes, "scope")
	})
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

	logger := zerolog.Nop()

	t.Run("empty map returns nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, marshalAuditDetails(logger, map[string]any{}))
		require.Nil(t, marshalAuditDetails(logger, nil))
	})

	t.Run("valid payload round-trips", func(t *testing.T) {
		t.Parallel()
		got := marshalAuditDetails(logger, map[string]any{"name": "x", "count": 3})
		require.NotNil(t, got)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(got, &decoded))
		require.Equal(t, "x", decoded["name"])
		require.Equal(t, float64(3), decoded["count"])
	})

	t.Run("unmarshalable payload returns nil and logs", func(t *testing.T) {
		t.Parallel()
		// Channels are not JSON-marshalable, so this exercises the error
		// branch. We verify both that the return is nil (so the audit row
		// stores SQL NULL rather than corrupt bytes) and that a log entry
		// was written so silent data loss is observable.
		var buf bytes.Buffer
		bufLogger := zerolog.New(&buf)
		details := map[string]any{"unencodable": make(chan int)}
		require.Nil(t, marshalAuditDetails(bufLogger, details))
		require.Contains(t, buf.String(), "marshal audit details")
	})
}
