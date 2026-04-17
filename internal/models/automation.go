package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Automation is a recurring, team-owned agent process.
// Unlike projects (which are finite and goal-oriented), automations run on a
// schedule and never "complete" — they are enabled or paused.
type Automation struct {
	ID             uuid.UUID  `db:"id"              json:"id"`
	OrgID          uuid.UUID  `db:"org_id"          json:"org_id"`
	RepositoryID   *uuid.UUID `db:"repository_id"   json:"repository_id,omitempty"`
	Name           string     `db:"name"            json:"name"`
	Goal           string     `db:"goal"            json:"goal"`
	Scope          *string    `db:"scope"           json:"scope,omitempty"`
	AgentType      *string    `db:"agent_type"      json:"agent_type,omitempty"`
	ModelOverride  *string    `db:"model_override"  json:"model_override,omitempty"`
	ExecutionMode  string     `db:"execution_mode"  json:"execution_mode"`
	MaxConcurrent  int        `db:"max_concurrent"  json:"max_concurrent"`
	BaseBranch     string     `db:"base_branch"     json:"base_branch"`
	ScheduleType   string     `db:"schedule_type"   json:"schedule_type"`
	IntervalValue  *int       `db:"interval_value"  json:"interval_value,omitempty"`
	IntervalUnit   *string    `db:"interval_unit"   json:"interval_unit,omitempty"`
	CronExpression *string    `db:"cron_expression" json:"cron_expression,omitempty"`
	Timezone       string     `db:"timezone"        json:"timezone"`
	NextRunAt      *time.Time `db:"next_run_at"     json:"next_run_at,omitempty"`
	LastRunAt      *time.Time `db:"last_run_at"     json:"last_run_at,omitempty"`
	Enabled        bool       `db:"enabled"         json:"enabled"`
	CreatedBy      *uuid.UUID `db:"created_by"      json:"created_by,omitempty"`
	PausedBy       *uuid.UUID `db:"paused_by"       json:"paused_by,omitempty"`
	PausedAt       *time.Time `db:"paused_at"       json:"paused_at,omitempty"`
	Priority       int        `db:"priority"        json:"priority"`
	CreatedAt      time.Time  `db:"created_at"      json:"created_at"`
	UpdatedAt      time.Time  `db:"updated_at"      json:"updated_at"`
	DeletedAt      *time.Time `db:"deleted_at"      json:"-"`
}

// AutomationRun records a single execution of an automation (scheduled or manual).
type AutomationRun struct {
	ID                uuid.UUID       `db:"id"                    json:"id"`
	AutomationID      uuid.UUID       `db:"automation_id"         json:"automation_id"`
	OrgID             uuid.UUID       `db:"org_id"                json:"org_id"`
	TriggeredAt       time.Time       `db:"triggered_at"          json:"triggered_at"`
	TriggeredBy       string          `db:"triggered_by"          json:"triggered_by"`
	TriggeredByUserID *uuid.UUID      `db:"triggered_by_user_id"  json:"triggered_by_user_id,omitempty"`
	ScheduledTime     *time.Time      `db:"scheduled_time"        json:"scheduled_time,omitempty"`
	GoalSnapshot      string          `db:"goal_snapshot"         json:"goal_snapshot"`
	ConfigSnapshot    json.RawMessage `db:"config_snapshot"       json:"config_snapshot,omitempty"`
	Status            string          `db:"status"                json:"status"`
	CompletedAt       *time.Time      `db:"completed_at"          json:"completed_at,omitempty"`
	ResultSummary     *string         `db:"result_summary"        json:"result_summary,omitempty"`
	CreatedAt         time.Time       `db:"created_at"            json:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"            json:"updated_at"`
}

// AutomationRunStatus constants.
const (
	AutomationRunStatusPending       = "pending"
	AutomationRunStatusRunning       = "running"
	AutomationRunStatusCompleted     = "completed"
	AutomationRunStatusCompletedNoop = "completed_noop"
	AutomationRunStatusFailed        = "failed"
	AutomationRunStatusSkipped       = "skipped"
)

// AutomationTriggeredBy constants.
const (
	AutomationTriggeredBySchedule = "schedule"
	AutomationTriggeredByManual   = "manual"
)

// AutomationScheduleType constants.
const (
	AutomationScheduleInterval = "interval"
	AutomationScheduleCron     = "cron"
)

// BuildConfigSnapshot returns the JSON config snapshot for an automation run.
//
// The current fields are all string / *string and json.Marshal can't fail for
// them, but returning an error keeps the contract honest: if a future field
// change introduces a non-marshalable type, the HTTP handler surfaces a 500
// instead of panicking inside chi middleware.
func (a *Automation) BuildConfigSnapshot() (json.RawMessage, error) {
	data, err := json.Marshal(map[string]any{
		"agent_type":     a.AgentType,
		"model_override": a.ModelOverride,
		"scope":          a.Scope,
		"base_branch":    a.BaseBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal automation config snapshot: %w", err)
	}
	return data, nil
}

func ValidateAutomationScheduleType(t string) error {
	switch t {
	case AutomationScheduleInterval, AutomationScheduleCron:
		return nil
	default:
		return fmt.Errorf("invalid schedule_type: %q (must be interval or cron)", t)
	}
}

func ValidateAutomationRunStatus(s string) error {
	switch s {
	case AutomationRunStatusPending, AutomationRunStatusRunning,
		AutomationRunStatusCompleted, AutomationRunStatusCompletedNoop,
		AutomationRunStatusFailed, AutomationRunStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid automation run status: %q", s)
	}
}
