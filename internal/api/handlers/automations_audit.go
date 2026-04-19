package handlers

import (
	"encoding/json"
	"reflect"

	"github.com/assembledhq/143/internal/models"
)

// automationAuditSnapshot returns a minimal key/value summary of an automation
// for audit log details. It is used for events that either create or destroy
// the row (create/delete/pause/resume/run-triggered) where a full diff is not
// meaningful but a human-readable name + schedule context is.
func automationAuditSnapshot(a *models.Automation) map[string]any {
	snap := map[string]any{
		"name":          a.Name,
		"schedule_type": a.ScheduleType,
	}
	switch a.ScheduleType {
	case models.AutomationScheduleInterval:
		if a.IntervalValue != nil {
			snap["interval_value"] = *a.IntervalValue
		}
		if a.IntervalUnit != nil {
			snap["interval_unit"] = *a.IntervalUnit
		}
	case models.AutomationScheduleCron:
		if a.CronExpression != nil {
			snap["cron_expression"] = *a.CronExpression
		}
		snap["timezone"] = a.Timezone
	}
	return snap
}

// automationAuditDiff reports every user-editable field whose value changed
// between old and new_. Unchanged fields are omitted. The returned map has the
// shape {"field": {"before": X, "after": Y}} so the audit viewer can render a
// clean side-by-side diff without the frontend needing field-specific logic.
//
// System-managed fields (next_run_at, last_run_at, paused_at, paused_by,
// enabled, updated_at, etc.) are intentionally excluded: they shift on every
// run or are handled by dedicated events (AutomationPaused / AutomationResumed)
// that would otherwise double-report them.
func automationAuditDiff(old, new_ *models.Automation) map[string]any {
	changes := map[string]any{}
	track := func(field string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			changes[field] = map[string]any{"before": a, "after": b}
		}
	}

	track("name", old.Name, new_.Name)
	track("goal", old.Goal, new_.Goal)
	track("scope", derefString(old.Scope), derefString(new_.Scope))
	track("agent_type", derefString(old.AgentType), derefString(new_.AgentType))
	track("model_override", derefString(old.ModelOverride), derefString(new_.ModelOverride))
	track("execution_mode", old.ExecutionMode, new_.ExecutionMode)
	track("max_concurrent", old.MaxConcurrent, new_.MaxConcurrent)
	track("base_branch", old.BaseBranch, new_.BaseBranch)
	track("schedule_type", old.ScheduleType, new_.ScheduleType)
	track("interval_value", derefInt(old.IntervalValue), derefInt(new_.IntervalValue))
	track("interval_unit", derefString(old.IntervalUnit), derefString(new_.IntervalUnit))
	track("cron_expression", derefString(old.CronExpression), derefString(new_.CronExpression))
	track("timezone", old.Timezone, new_.Timezone)
	track("priority", old.Priority, new_.Priority)

	oldRepo, newRepo := "", ""
	if old.RepositoryID != nil {
		oldRepo = old.RepositoryID.String()
	}
	if new_.RepositoryID != nil {
		newRepo = new_.RepositoryID.String()
	}
	track("repository_id", oldRepo, newRepo)

	return changes
}

// marshalAuditDetails JSON-encodes a details map. Returns nil (which the audit
// writer treats as SQL NULL) for empty input so we don't spam audit rows with
// "{}" blobs that the UI would have to special-case.
func marshalAuditDetails(details map[string]any) json.RawMessage {
	if len(details) == 0 {
		return nil
	}
	b, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return b
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
