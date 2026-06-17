package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/gorhill/cronexpr"
)

// Automation is a recurring, team-owned agent process.
// Unlike projects (which are finite and goal-oriented), automations run on a
// schedule and never "complete" — they are enabled or paused.
type Automation struct {
	ID               uuid.UUID               `db:"id"               json:"id"`
	OrgID            uuid.UUID               `db:"org_id"           json:"org_id"`
	RepositoryID     *uuid.UUID              `db:"repository_id"    json:"repository_id,omitempty"`
	Name             string                  `db:"name"             json:"name"`
	Goal             string                  `db:"goal"             json:"goal"`
	Scope            *string                 `db:"scope"            json:"scope,omitempty"`
	IconType         AutomationIconType      `db:"icon_type"        json:"icon_type"`
	IconValue        string                  `db:"icon_value"       json:"icon_value"`
	AgentType        *string                 `db:"agent_type"       json:"agent_type,omitempty"`
	ModelOverride    *string                 `db:"model_override"   json:"model_override,omitempty"`
	ReasoningEffort  *ReasoningEffort        `db:"reasoning_effort" json:"reasoning_effort,omitempty"`
	ExecutionMode    AutomationExecutionMode `db:"execution_mode"   json:"execution_mode"`
	MaxConcurrent    int                     `db:"max_concurrent"   json:"max_concurrent"`
	BaseBranch       string                  `db:"base_branch"      json:"base_branch"`
	IdentityScope    AutomationIdentityScope `db:"identity_scope"   json:"identity_scope"`
	PrePRReviewLoops int                     `db:"pre_pr_review_loops" json:"pre_pr_review_loops"`
	ScheduleType     AutomationScheduleType  `db:"schedule_type"    json:"schedule_type"`
	IntervalValue    *int                    `db:"interval_value"   json:"interval_value,omitempty"`
	IntervalUnit     *ScheduleUnit           `db:"interval_unit"    json:"interval_unit,omitempty"`
	IntervalRunAt    *string                 `db:"interval_run_at"  json:"interval_run_at,omitempty"`
	CronExpression   *string                 `db:"cron_expression"  json:"cron_expression,omitempty"`
	// Timezone is the IANA zone used to evaluate wall-clock schedule targets:
	// cron_expression for cron rows, and interval_run_at for interval rows
	// that specify one. An interval row without interval_run_at uses pure
	// duration arithmetic (NextRunTime) and the stored timezone is inert.
	// Migration 93 dropped the chk_automations_timezone_interval DB CHECK so
	// interval rows can now carry non-UTC zones; writers must still set
	// timezone='UTC' only when meaningful.
	Timezone            string                  `db:"timezone"        json:"timezone"`
	GitHubEventTriggers []AutomationGitHubEvent `db:"github_event_triggers" json:"github_event_triggers,omitempty"`
	NextRunAt           *time.Time              `db:"next_run_at"     json:"next_run_at,omitempty"`
	LastRunAt           *time.Time              `db:"last_run_at"     json:"last_run_at,omitempty"`
	Enabled             bool                    `db:"enabled"         json:"enabled"`
	CreatedBy           *uuid.UUID              `db:"created_by"      json:"created_by,omitempty"`
	PausedBy            *uuid.UUID              `db:"paused_by"       json:"paused_by,omitempty"`
	PausedAt            *time.Time              `db:"paused_at"       json:"paused_at,omitempty"`
	Priority            int                     `db:"priority"        json:"priority"`
	ExternalMetadata    json.RawMessage         `db:"external_metadata" json:"metadata,omitempty"`
	CreatedAt           time.Time               `db:"created_at"      json:"created_at"`
	UpdatedAt           time.Time               `db:"updated_at"      json:"updated_at"`
	DeletedAt           *time.Time              `db:"deleted_at"      json:"-"`
}

// AutomationRun records a single execution of an automation (scheduled or manual).
type AutomationRun struct {
	ID                 uuid.UUID                     `db:"id"                    json:"id"`
	AutomationID       uuid.UUID                     `db:"automation_id"         json:"automation_id"`
	OrgID              uuid.UUID                     `db:"org_id"                json:"org_id"`
	TriggeredAt        time.Time                     `db:"triggered_at"          json:"triggered_at"`
	TriggeredBy        AutomationTriggeredBy         `db:"triggered_by"          json:"triggered_by"`
	TriggeredByUserID  *uuid.UUID                    `db:"triggered_by_user_id"  json:"triggered_by_user_id,omitempty"`
	ScheduledTime      *time.Time                    `db:"scheduled_time"        json:"scheduled_time,omitempty"`
	GoalSnapshot       string                        `db:"goal_snapshot"         json:"goal_snapshot"`
	ConfigSnapshot     json.RawMessage               `db:"config_snapshot"       json:"config_snapshot,omitempty"`
	Status             AutomationRunStatus           `db:"status"                json:"status"`
	CapabilitySnapshot []AgentCapabilitySnapshotItem `db:"capability_snapshot" json:"capability_snapshot,omitempty"`
	CompletedAt        *time.Time                    `db:"completed_at"          json:"completed_at,omitempty"`
	ResultSummary      *string                       `db:"result_summary"        json:"result_summary,omitempty"`
	CreatedAt          time.Time                     `db:"created_at"            json:"created_at"`
	UpdatedAt          time.Time                     `db:"updated_at"            json:"updated_at"`

	// Session is a compact view of the session this run spawned, populated
	// only by list/detail endpoints that join sessions (currently
	// ListByAutomation). It is left nil by single-row fetches like GetByID
	// and by writers (insertRun, scanAutomationRun for the bare table) so
	// the on-the-wire shape stays additive: pre-existing consumers that
	// don't read the field continue to work, and new consumers can rely on
	// it to render row detail without an N+1.
	Session *AutomationRunSession `json:"session,omitempty"`
}

// AutomationRunSession is the slice of the spawned session that the
// automation runs list surfaces inline. Mirrors SessionListItem's PRSummary
// pattern: a deliberately small projection so we can carry it on every
// listed run without ballooning the payload during 10s polling.
//
// Fields here are read-only views of sessions / pull_requests rows; nothing
// in this struct is persisted directly. The Session field on AutomationRun
// is nil when no session has been spawned yet (pending/skipped runs).
type AutomationRunSession struct {
	ID                  uuid.UUID       `json:"id"`
	Title               *string         `json:"title,omitempty"`
	Status              SessionStatus   `json:"status"`
	DiffStats           json.RawMessage `json:"diff_stats,omitempty"`
	FailureExplanation  *string         `json:"failure_explanation,omitempty"`
	FailureCategory     *string         `json:"failure_category,omitempty"`
	FailureNextSteps    []string        `json:"failure_next_steps,omitempty"`
	FailureRetryAdvised bool            `json:"failure_retry_advised"`
	PRCreationState     PRCreationState `json:"pr_creation_state"`
	// PR is populated only when a PullRequest row exists for this session.
	// Reuses models.PRSummary so the frontend can share rendering with the
	// session list page.
	PR *PRSummary `json:"pr,omitempty"`
}

type AutomationExecutionMode string

const (
	AutomationExecutionModeSequential      AutomationExecutionMode = "sequential"
	AutomationExecutionModeParallel        AutomationExecutionMode = "parallel"
	AutomationExecutionModeDependencyGraph AutomationExecutionMode = "dependency_graph"
)

func (m AutomationExecutionMode) Validate() error {
	switch m {
	case AutomationExecutionModeSequential, AutomationExecutionModeParallel, AutomationExecutionModeDependencyGraph:
		return nil
	default:
		return fmt.Errorf("invalid automation execution mode: %q", m)
	}
}

type AutomationRunStatus string

const (
	AutomationRunStatusPending       AutomationRunStatus = "pending"
	AutomationRunStatusRunning       AutomationRunStatus = "running"
	AutomationRunStatusCompleted     AutomationRunStatus = "completed"
	AutomationRunStatusCompletedNoop AutomationRunStatus = "completed_noop"
	AutomationRunStatusFailed        AutomationRunStatus = "failed"
	AutomationRunStatusSkipped       AutomationRunStatus = "skipped"
)

type AutomationTriggeredBy string

const (
	AutomationTriggeredBySchedule AutomationTriggeredBy = "schedule"
	AutomationTriggeredByManual   AutomationTriggeredBy = "manual"
	AutomationTriggeredByGitHub   AutomationTriggeredBy = "github"
)

func (t AutomationTriggeredBy) Validate() error {
	switch t {
	case AutomationTriggeredBySchedule, AutomationTriggeredByManual, AutomationTriggeredByGitHub:
		return nil
	default:
		return fmt.Errorf("invalid automation triggered_by: %q", t)
	}
}

type AutomationGitHubEvent string

const (
	AutomationGitHubEventPullRequestOpened               AutomationGitHubEvent = "github.pull_request.opened"
	AutomationGitHubEventIssueCommentCreated             AutomationGitHubEvent = "github.issue_comment.created"
	AutomationGitHubEventPullRequestReviewSubmitted      AutomationGitHubEvent = "github.pull_request_review.submitted"
	AutomationGitHubEventPullRequestReviewCommentCreated AutomationGitHubEvent = "github.pull_request_review_comment.created"
)

func (e AutomationGitHubEvent) Validate() error {
	switch e {
	case AutomationGitHubEventPullRequestOpened,
		AutomationGitHubEventIssueCommentCreated,
		AutomationGitHubEventPullRequestReviewSubmitted,
		AutomationGitHubEventPullRequestReviewCommentCreated:
		return nil
	default:
		return fmt.Errorf("invalid automation github event: %q", e)
	}
}

type AutomationScheduleType string

const (
	AutomationScheduleInterval AutomationScheduleType = "interval"
	AutomationScheduleCron     AutomationScheduleType = "cron"
)

// AutomationIdentityScope controls whose credentials an automation uses when
// it spawns sessions and creates pull requests.
type AutomationIdentityScope string

const (
	AutomationIdentityScopeOrg      AutomationIdentityScope = "org"
	AutomationIdentityScopePersonal AutomationIdentityScope = "personal"
)

func (s AutomationIdentityScope) Validate() error {
	switch s {
	case "", AutomationIdentityScopeOrg, AutomationIdentityScopePersonal:
		return nil
	default:
		return fmt.Errorf("invalid identity_scope: %q (must be org or personal)", s)
	}
}

func (s AutomationIdentityScope) OrDefault() AutomationIdentityScope {
	if s == "" {
		return AutomationIdentityScopeOrg
	}
	return s
}

// AutomationIconType is intentionally separated from IconValue so future
// image-backed automation icons can reuse the same API shape without changing
// callers that already persist a typed visual identity.
type AutomationIconType string

const (
	AutomationIconTypeEmoji AutomationIconType = "emoji"

	DefaultAutomationIconValue = "⚙️"
)

func (t AutomationIconType) Validate() error {
	switch t {
	case "", AutomationIconTypeEmoji:
		return nil
	default:
		return fmt.Errorf("invalid icon_type: %q (must be emoji)", t)
	}
}

func (t AutomationIconType) OrDefault() AutomationIconType {
	if t == "" {
		return AutomationIconTypeEmoji
	}
	return t
}

func AutomationIconValueOrDefault(v string) string {
	if v == "" {
		return DefaultAutomationIconValue
	}
	return v
}

// BuildConfigSnapshot returns the JSON config snapshot for an automation run.
//
// The current fields are all string / *string and json.Marshal can't fail for
// them, but returning an error keeps the contract honest: if a future field
// change introduces a non-marshalable type, the HTTP handler surfaces a 500
// instead of panicking inside chi middleware.
func (a *Automation) BuildConfigSnapshot() (json.RawMessage, error) {
	data, err := json.Marshal(map[string]any{
		"agent_type":          a.AgentType,
		"model_override":      a.ModelOverride,
		"reasoning_effort":    a.ReasoningEffort,
		"scope":               a.Scope,
		"identity_scope":      a.IdentityScope.OrDefault(),
		"pre_pr_review_loops": a.PrePRReviewLoops,
		"base_branch":         a.BaseBranch,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal automation config snapshot: %w", err)
	}
	return data, nil
}

func (t AutomationScheduleType) Validate() error {
	switch t {
	case AutomationScheduleInterval, AutomationScheduleCron:
		return nil
	default:
		return fmt.Errorf("invalid schedule_type: %q (must be interval or cron)", t)
	}
}

func ValidateAutomationScheduleType(t string) error {
	return AutomationScheduleType(t).Validate()
}

// ValidateCronExpression parses the expression using gorhill/cronexpr so we
// reject malformed schedules at write time instead of silently letting a row
// sit un-scheduled. gorhill/cronexpr accepts both the standard 5-field form
// and an extended 6-field (with seconds) form, plus @yearly/@monthly/@weekly/
// @daily/@hourly aliases.
func ValidateCronExpression(expr string) error {
	if expr == "" {
		return fmt.Errorf("cron_expression must not be empty")
	}
	if _, err := cronexpr.Parse(expr); err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	return nil
}

// ValidateIntervalRunAt checks HH:MM (24h) time strings aligned to 5 minutes.
func ValidateIntervalRunAt(v string) error {
	if len(v) != len("15:04") {
		return fmt.Errorf("interval_run_at must be in HH:MM format")
	}
	parsed, err := time.Parse("15:04", v)
	if err != nil {
		return fmt.Errorf("interval_run_at must be in HH:MM format")
	}
	if parsed.Minute()%5 != 0 {
		return fmt.Errorf("interval_run_at minute must be divisible by 5")
	}
	return nil
}

// NextCronRunTime returns the next fire time for the cron expression in the
// given IANA timezone. Returns an error if the expression is malformed, the
// timezone is unknown, or the cron has no future occurrences (e.g. a fixed-
// date expression in the past).
//
// DST handling is delegated to cronexpr.Next which evaluates the schedule in
// the provided location — ambiguous/nonexistent local times resolve to the
// first valid occurrence.
func NextCronRunTime(expr, timezone string, from time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	parsed, err := cronexpr.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron expression: %w", err)
	}
	next := parsed.Next(from.In(loc))
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q has no future occurrences after %s", expr, from.Format(time.RFC3339))
	}
	return next.UTC(), nil
}

// ComputeNextRunAt is the single entry point both the API and the scheduler
// use to advance an automation's fire time. Centralising the branch on
// schedule_type here means a future schedule kind (event-based, combined
// interval+cron, etc.) only has to be added once.
func (a *Automation) ComputeNextRunAt(from time.Time) (time.Time, error) {
	switch a.ScheduleType {
	case AutomationScheduleInterval:
		if a.IntervalValue == nil || a.IntervalUnit == nil {
			return time.Time{}, fmt.Errorf("interval schedule requires interval_value and interval_unit")
		}
		if a.IntervalRunAt != nil && *a.IntervalRunAt != "" {
			if err := ValidateIntervalRunAt(*a.IntervalRunAt); err != nil {
				return time.Time{}, err
			}
			return NextRunTimeAt(from, *a.IntervalValue, string(*a.IntervalUnit), *a.IntervalRunAt, a.Timezone)
		}
		return NextRunTime(from, *a.IntervalValue, string(*a.IntervalUnit)), nil
	case AutomationScheduleCron:
		if a.CronExpression == nil || *a.CronExpression == "" {
			return time.Time{}, fmt.Errorf("cron schedule requires cron_expression")
		}
		tz := a.Timezone
		if tz == "" {
			tz = "UTC"
		}
		return NextCronRunTime(*a.CronExpression, tz, from)
	default:
		return time.Time{}, fmt.Errorf("unknown schedule_type: %q", a.ScheduleType)
	}
}

func (s AutomationRunStatus) Validate() error {
	switch s {
	case AutomationRunStatusPending, AutomationRunStatusRunning,
		AutomationRunStatusCompleted, AutomationRunStatusCompletedNoop,
		AutomationRunStatusFailed, AutomationRunStatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid automation run status: %q", s)
	}
}

func ValidateAutomationRunStatus(s string) error {
	return AutomationRunStatus(s).Validate()
}

// AutomationRunStatsBucket is a per-day aggregate over automation_runs. Dates
// are date_trunc('day', triggered_at AT TIME ZONE 'UTC') values rendered in
// RFC3339 at the day boundary.
//
// AvgDurationSeconds is computed over rows with completed_at set; it is 0
// when no run completed in the bucket.
type AutomationRunStatsBucket struct {
	Bucket             time.Time `json:"bucket"`
	Total              int       `json:"total"`
	Completed          int       `json:"completed"`
	CompletedNoop      int       `json:"completed_noop"`
	Failed             int       `json:"failed"`
	Skipped            int       `json:"skipped"`
	Running            int       `json:"running"`
	Pending            int       `json:"pending"`
	AvgDurationSeconds float64   `json:"avg_duration_seconds"`
}

// AutomationRunStatsTotals summarises the entire window covered by a stats
// query. SuccessRate is (completed + completed_noop) / (completed +
// completed_noop + failed), i.e. it excludes pending/running/skipped from
// both numerator and denominator — skipped runs indicate the schedule fired
// but the automation was paused, not a failure. It is 0 when no terminal
// runs exist (denominator zero).
type AutomationRunStatsTotals struct {
	Total              int     `json:"total"`
	Completed          int     `json:"completed"`
	CompletedNoop      int     `json:"completed_noop"`
	Failed             int     `json:"failed"`
	Skipped            int     `json:"skipped"`
	Running            int     `json:"running"`
	Pending            int     `json:"pending"`
	SuccessRate        float64 `json:"success_rate"`
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
}

type AutomationRunStats struct {
	Since   time.Time                  `json:"since"`
	Until   time.Time                  `json:"until"`
	Buckets []AutomationRunStatsBucket `json:"buckets"`
	Totals  AutomationRunStatsTotals   `json:"totals"`
}
