package handlers

import (
	"encoding/json"
	"reflect"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// automationAuditSnapshot returns a minimal key/value summary of an automation
// for audit log details. It is used for events that either create or destroy
// the row (create/delete/pause/resume/run-triggered) where a full diff is not
// meaningful but a human-readable name + schedule context is.
func automationAuditSnapshot(a *models.Automation) map[string]any {
	snap := map[string]any{
		"name":           a.Name,
		"icon_type":      a.IconType.OrDefault(),
		"icon_value":     a.IconValue,
		"identity_scope": a.IdentityScope.OrDefault(),
		"schedule_type":  a.ScheduleType,
	}
	if len(a.GitHubEventTriggers) > 0 {
		snap["triggers"] = automationProductTriggerSummary(a.GitHubEventTriggers)
		snap["github_event_triggers"] = automationGitHubEventStrings(a.GitHubEventTriggers)
	}
	if len(a.GitHubEventFilters) > 0 && string(a.GitHubEventFilters) != "{}" {
		snap["github_event_filters"] = json.RawMessage(a.GitHubEventFilters)
	}
	switch a.ScheduleType {
	case models.AutomationScheduleInterval:
		if a.IntervalValue != nil {
			snap["interval_value"] = *a.IntervalValue
		}
		if a.IntervalUnit != nil {
			snap["interval_unit"] = string(*a.IntervalUnit)
		}
		if a.IntervalRunAt != nil {
			snap["interval_run_at"] = *a.IntervalRunAt
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
	// Optional fields use tri-state (nil vs set) so nil→"" and 0→nil
	// transitions are visible in the audit diff rather than collapsed away.
	track("scope", optString(old.Scope), optString(new_.Scope))
	track("icon_type", old.IconType.OrDefault(), new_.IconType.OrDefault())
	track("icon_value", old.IconValue, new_.IconValue)
	track("agent_type", optString(old.AgentType), optString(new_.AgentType))
	track("model_override", optString(old.ModelOverride), optString(new_.ModelOverride))
	track("execution_mode", old.ExecutionMode, new_.ExecutionMode)
	track("max_concurrent", old.MaxConcurrent, new_.MaxConcurrent)
	track("base_branch", old.BaseBranch, new_.BaseBranch)
	track("identity_scope", old.IdentityScope.OrDefault(), new_.IdentityScope.OrDefault())
	track("schedule_type", old.ScheduleType, new_.ScheduleType)
	track("interval_value", optInt(old.IntervalValue), optInt(new_.IntervalValue))
	track("interval_unit", optScheduleUnit(old.IntervalUnit), optScheduleUnit(new_.IntervalUnit))
	track("interval_run_at", optString(old.IntervalRunAt), optString(new_.IntervalRunAt))
	track("cron_expression", optString(old.CronExpression), optString(new_.CronExpression))
	track("timezone", old.Timezone, new_.Timezone)
	track("github_event_triggers", automationGitHubEventStrings(old.GitHubEventTriggers), automationGitHubEventStrings(new_.GitHubEventTriggers))
	track("triggers", automationProductTriggerSummary(old.GitHubEventTriggers), automationProductTriggerSummary(new_.GitHubEventTriggers))
	track("github_event_filters", auditJSONRaw(old.GitHubEventFilters), auditJSONRaw(new_.GitHubEventFilters))
	track("priority", old.Priority, new_.Priority)
	track("repository_id", optUUIDString(old.RepositoryID), optUUIDString(new_.RepositoryID))

	return changes
}

func automationProductTriggerSummary(events []models.AutomationGitHubEvent) []string {
	if len(events) == 0 {
		return nil
	}
	eventSet := map[models.AutomationGitHubEvent]struct{}{}
	for _, event := range events {
		eventSet[event] = struct{}{}
	}
	var out []string
	if _, ok := eventSet[models.AutomationGitHubEventPullRequestOpened]; ok {
		out = append(out, string(models.AutomationProductTriggerPROpened))
	}
	if _, ok := eventSet[models.AutomationGitHubEventPullRequestUpdated]; ok {
		out = append(out, string(models.AutomationProductTriggerPRUpdated))
	}
	if _, ok := eventSet[models.AutomationGitHubEventIssueCommentCreated]; ok {
		out = append(out, string(models.AutomationProductTriggerPRFeedback))
	} else if _, ok := eventSet[models.AutomationGitHubEventPullRequestReviewSubmitted]; ok {
		out = append(out, string(models.AutomationProductTriggerPRFeedback))
	} else if _, ok := eventSet[models.AutomationGitHubEventPullRequestReviewCommentCreated]; ok {
		out = append(out, string(models.AutomationProductTriggerPRFeedback))
	}
	if _, ok := eventSet[models.AutomationGitHubEventCheckSuiteCompleted]; ok {
		out = append(out, string(models.AutomationProductTriggerChecksCompleted))
	} else if _, ok := eventSet[models.AutomationGitHubEventCheckRunCompleted]; ok {
		out = append(out, string(models.AutomationProductTriggerChecksCompleted))
	}
	if _, ok := eventSet[models.AutomationGitHubEventPullRequestMerged]; ok {
		out = append(out, string(models.AutomationProductTriggerPRMerged))
	}
	return out
}

func auditJSONRaw(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw)
	}
	return decoded
}

func automationGitHubEventStrings(events []models.AutomationGitHubEvent) []string {
	if len(events) == 0 {
		return nil
	}
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, string(event))
	}
	return out
}

// marshalAuditDetails JSON-encodes a details map. Returns nil (which the audit
// writer treats as SQL NULL) for empty input so we don't spam audit rows with
// "{}" blobs that the UI would have to special-case. Marshal failures are
// logged so silent audit data loss surfaces during incident review.
func marshalAuditDetails(logger zerolog.Logger, details map[string]any) json.RawMessage {
	if len(details) == 0 {
		return nil
	}
	b, err := json.Marshal(details)
	if err != nil {
		logger.Error().Err(err).Interface("details", details).Msg("marshal audit details")
		return nil
	}
	return b
}

// optString/optInt/optUUIDString return nil for nil pointers and the
// dereferenced value otherwise. Audit diffs use this so a nil→"" transition
// (e.g. clearing an optional field) is distinguishable from a no-op; a plain
// deref would collapse both sides to "" and hide the change.
func optString(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func optScheduleUnit(p *models.ScheduleUnit) any {
	if p == nil {
		return nil
	}
	return string(*p)
}

func optInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func optUUIDString(p *uuid.UUID) any {
	if p == nil {
		return nil
	}
	return p.String()
}
