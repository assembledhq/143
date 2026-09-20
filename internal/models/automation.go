package models

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorhill/cronexpr"
)

// Automation is a recurring, team-owned agent process.
// Unlike projects (which are finite and goal-oriented), automations run on a
// schedule and never "complete" — they are enabled or paused.
type Automation struct {
	ID              uuid.UUID          `db:"id"               json:"id"`
	OrgID           uuid.UUID          `db:"org_id"           json:"org_id"`
	RepositoryID    *uuid.UUID         `db:"repository_id"    json:"repository_id,omitempty"`
	Name            string             `db:"name"             json:"name"`
	Goal            string             `db:"goal"             json:"goal"`
	Scope           *string            `db:"scope"            json:"scope,omitempty"`
	IconType        AutomationIconType `db:"icon_type"        json:"icon_type"`
	IconValue       string             `db:"icon_value"       json:"icon_value"`
	AgentType       *string            `db:"agent_type"       json:"agent_type,omitempty"`
	ModelOverride   *string            `db:"model_override"   json:"model_override,omitempty"`
	ReasoningEffort *ReasoningEffort   `db:"reasoning_effort" json:"reasoning_effort,omitempty"`
	// FallbackModels holds ranks 1..N of the model chain; rank 0 is the
	// AgentType/ModelOverride/ReasoningEffort trio above. Read the whole chain
	// through ModelRanks rather than these fields directly.
	FallbackModels   AutomationFallbackModels `db:"fallback_models"  json:"fallback_models,omitzero"`
	ExecutionMode    AutomationExecutionMode  `db:"execution_mode"   json:"execution_mode"`
	MaxConcurrent    int                      `db:"max_concurrent"   json:"max_concurrent"`
	BaseBranch       string                   `db:"base_branch"      json:"base_branch"`
	IdentityScope    AutomationIdentityScope  `db:"identity_scope"   json:"identity_scope"`
	PublishPolicy    AutomationPublishPolicy  `db:"publish_policy"   json:"publish_policy"`
	PrePRReviewLoops int                      `db:"pre_pr_review_loops" json:"pre_pr_review_loops"`
	ScheduleType     AutomationScheduleType   `db:"schedule_type"    json:"schedule_type"`
	IntervalValue    *int                     `db:"interval_value"   json:"interval_value,omitempty"`
	IntervalUnit     *ScheduleUnit            `db:"interval_unit"    json:"interval_unit,omitempty"`
	IntervalRunAt    *string                  `db:"interval_run_at"  json:"interval_run_at,omitempty"`
	CronExpression   *string                  `db:"cron_expression"  json:"cron_expression,omitempty"`
	// Timezone is the IANA zone used to evaluate wall-clock schedule targets:
	// cron_expression for cron rows, and interval_run_at for interval rows
	// that specify one. An interval row without interval_run_at uses pure
	// duration arithmetic (NextRunTime) and the stored timezone is inert.
	// Migration 93 dropped the chk_automations_timezone_interval DB CHECK so
	// interval rows can now carry non-UTC zones; writers must still set
	// timezone='UTC' only when meaningful.
	Timezone            string                   `db:"timezone"        json:"timezone"`
	GitHubEventTriggers []AutomationGitHubEvent  `db:"github_event_triggers" json:"github_event_triggers,omitempty"`
	GitHubEventFilters  json.RawMessage          `db:"github_event_filters" json:"github_event_filters,omitempty"`
	EventTriggers       []AutomationEventTrigger `json:"event_triggers,omitempty"`
	NextRunAt           *time.Time               `db:"next_run_at"     json:"next_run_at,omitempty"`
	LastRunAt           *time.Time               `db:"last_run_at"     json:"last_run_at,omitempty"`
	Enabled             bool                     `db:"enabled"         json:"enabled"`
	CreatedBy           *uuid.UUID               `db:"created_by"      json:"created_by,omitempty"`
	PausedBy            *uuid.UUID               `db:"paused_by"       json:"paused_by,omitempty"`
	PausedAt            *time.Time               `db:"paused_at"       json:"paused_at,omitempty"`
	Priority            int                      `db:"priority"        json:"priority"`
	ExternalMetadata    json.RawMessage          `db:"external_metadata" json:"metadata,omitempty"`
	CreatedAt           time.Time                `db:"created_at"      json:"created_at"`
	UpdatedAt           time.Time                `db:"updated_at"      json:"updated_at"`
	DeletedAt           *time.Time               `db:"deleted_at"      json:"-"`
}

// MaxAutomationFallbackModels bounds the ranks stored beyond the primary, so a
// run can attempt at most five models in total.
//
// Deliberately smaller than MaxCodeReviewReviewerModels (10): a code-review
// rank is one reviewer thread running in parallel, whereas an automation rank
// is a full sequential agent run. All ranks share one run's wall clock —
// ReapStuckRuns keys off triggered_at — so the ceiling has to fit inside it.
const MaxAutomationFallbackModels = 4

// AutomationFallbackModels holds ranks 1..N of an automation's model chain as
// index-aligned arrays, mirroring CodeReviewAgentRoster's parallel-array shape.
// Rank 0 is not stored here: it stays in the automation's own agent_type,
// model_override and reasoning_effort columns, which the worker, the config
// snapshot, the audit diff, MCP and both frontend pages already read. Storing
// it twice would create a dual-write invariant across all of them.
//
// AgentTypes and ReasoningEfforts are each either empty — meaning "derive per
// rank" — or exactly as long as Models. Use ModelRanks to read the chain; it
// materializes the flat ordered list that validation, the runtime and the UI
// all work from.
type AutomationFallbackModels struct {
	AgentTypes       []string          `json:"agent_types,omitempty"`
	Models           []string          `json:"models,omitempty"`
	ReasoningEfforts []ReasoningEffort `json:"reasoning_efforts,omitempty"`
}

// AutomationRunAttempt is one model attempt an automation run has already
// spent: the session it spawned, the agent and model that session actually ran
// on, and whether it left work behind.
//
// Field order is load-bearing — the store scans it positionally.
type AutomationRunAttempt struct {
	SessionID           uuid.UUID
	AgentType           *string
	Model               *string
	ReasoningEffort     *string
	ProducedDiff        bool
	ProducedPullRequest bool
}

// ProducedWork reports whether this attempt left something behind that a retry
// on another model would duplicate.
func (a AutomationRunAttempt) ProducedWork() bool {
	return a.ProducedDiff || a.ProducedPullRequest
}

// Matches reports whether a ranked candidate names the same (agent, model,
// reasoning effort) this attempt already ran.
//
// Reasoning effort is part of the identity because the chain deliberately
// supports re-trying one model at a cheaper level; comparing only (agent,
// model) would mark that fallback spent the moment the first level ran, and it
// would never get a session. The candidate list draws the same line.
//
// Callers must hand this a rank whose agent is already RESOLVED to a concrete
// value. A session records the agent it actually ran, never a blank one, so a
// rank still carrying nil can never match its own attempt — which would spin
// the chain re-dispatching the same model until the reaper stopped it.
func (a AutomationRunAttempt) Matches(rank AutomationModelRank) bool {
	return strings.TrimSpace(stringOrEmpty(a.AgentType)) == strings.TrimSpace(stringOrEmpty(rank.AgentType)) &&
		strings.TrimSpace(stringOrEmpty(a.Model)) == strings.TrimSpace(stringOrEmpty(rank.Model)) &&
		strings.TrimSpace(stringOrEmpty(a.ReasoningEffort)) == strings.TrimSpace(reasoningEffortOrEmpty(rank.ReasoningEffort))
}

func reasoningEffortOrEmpty(v *ReasoningEffort) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

// AutomationModelRanksRemaining returns the ranks this run has not yet spent a
// session on, in chain order.
//
// Each attempt consumes the EARLIEST unspent rank it matches, oldest attempt
// first, rather than every rank it happens to equal. That is what lets a chain
// deliberately return to an earlier model — A then B then A — actually reach
// its third rank: the first A consumes rank 0 and leaves rank 2 for later.
//
// attempts arrive newest-first (the order the store returns them), so this
// walks them in reverse to replay dispatch order.
//
// The worker and the promotion hook both call this. They used to carry
// separate implementations and disagreed about a chain's length, which let the
// hook promote a run the worker then immediately failed as exhausted.
func AutomationModelRanksRemaining(ranks []AutomationModelRank, attempts []AutomationRunAttempt) []AutomationModelRank {
	if len(attempts) == 0 {
		return ranks
	}
	spent := make([]bool, len(ranks))
	for i := len(attempts) - 1; i >= 0; i-- {
		for idx := range ranks {
			if !spent[idx] && attempts[i].Matches(ranks[idx]) {
				spent[idx] = true
				break
			}
		}
	}
	remaining := make([]AutomationModelRank, 0, len(ranks))
	for idx, rank := range ranks {
		if !spent[idx] {
			remaining = append(remaining, rank)
		}
	}
	return remaining
}

func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// AutomationModelRank is one entry in an automation's ordered model chain.
// Rank 0 (Fallback false) is the configured primary; the rest are fallbacks
// tried in order when an earlier rank has no usable credential or fails with a
// capacity error.
type AutomationModelRank struct {
	AgentType       *string          `json:"agent_type,omitempty"`
	Model           *string          `json:"model,omitempty"`
	ReasoningEffort *ReasoningEffort `json:"reasoning_effort,omitempty"`
	Fallback        bool             `json:"fallback"`
}

// Len returns the number of configured fallback ranks.
func (f AutomationFallbackModels) Len() int {
	return len(f.Models)
}

// AgentTypeAt resolves the agent for one fallback rank: the explicit entry,
// else the agent the model name itself implies, else the automation's primary
// agent. Mirrors the explicit → legacy → default tiering of
// CodeReviewAgentRoster.ReviewerReasoningEffort.
func (f AutomationFallbackModels) AgentTypeAt(index int, primary *string) *string {
	if index >= 0 && index < len(f.AgentTypes) && strings.TrimSpace(f.AgentTypes[index]) != "" {
		trimmed := strings.TrimSpace(f.AgentTypes[index])
		return &trimmed
	}
	if index >= 0 && index < len(f.Models) {
		if inferred := AgentTypeForModel(strings.TrimSpace(f.Models[index])); inferred != "" {
			s := string(inferred)
			return &s
		}
	}
	return primary
}

// ReasoningEffortAt resolves the effort for one fallback rank: the explicit
// entry, else the automation's primary effort, else nil so the agent's own
// default applies.
//
// An INHERITED effort is dropped when the rank's own agent cannot run it.
// Inheritance is a convenience for the common same-agent case, not a
// constraint: without this, setting the primary to Claude Code at "max" would
// make every Codex fallback unsavable (Codex has no "max"), and an Amp or Pi
// fallback unsavable at any effort, since those agents accept none. An
// EXPLICIT per-rank effort is returned untouched so the user still gets a clear
// error for their own typo rather than a silent downgrade.
func (f AutomationFallbackModels) ReasoningEffortAt(index int, primary *ReasoningEffort, agentType *string) *ReasoningEffort {
	if index >= 0 && index < len(f.ReasoningEfforts) && f.ReasoningEfforts[index] != "" {
		effort := f.ReasoningEfforts[index]
		return &effort
	}
	if primary == nil || *primary == "" {
		return nil
	}
	if agentType == nil || !AgentType(strings.TrimSpace(*agentType)).SupportsReasoningEffortLevel(*primary) {
		return nil
	}
	return primary
}

// Normalize trims every entry and drops arrays that carry no information, so a
// round-trip through the API and the database produces one canonical encoding.
// Without it the audit diff's reflect.DeepEqual reports spurious changes
// between nil and empty slices on no-op PATCHes.
func (f AutomationFallbackModels) Normalize() AutomationFallbackModels {
	if len(f.Models) == 0 {
		return AutomationFallbackModels{}
	}
	normalized := AutomationFallbackModels{Models: make([]string, len(f.Models))}
	for i, model := range f.Models {
		normalized.Models[i] = strings.TrimSpace(model)
	}
	if hasNonEmptyString(f.AgentTypes) {
		normalized.AgentTypes = make([]string, len(f.Models))
		for i := range normalized.AgentTypes {
			if i < len(f.AgentTypes) {
				normalized.AgentTypes[i] = strings.TrimSpace(f.AgentTypes[i])
			}
		}
	}
	if hasNonEmptyEffort(f.ReasoningEfforts) {
		normalized.ReasoningEfforts = make([]ReasoningEffort, len(f.Models))
		for i := range normalized.ReasoningEfforts {
			if i < len(f.ReasoningEfforts) {
				normalized.ReasoningEfforts[i] = f.ReasoningEfforts[i]
			}
		}
	}
	return normalized
}

func hasNonEmptyString(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func hasNonEmptyEffort(values []ReasoningEffort) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

// Validate checks the ranked chain against the automation's primary agent.
//
// Every message contains the word "model" on purpose: the automations handlers
// classify validation failures by substring to choose between INVALID_MODEL and
// INVALID_AGENT_TYPE.
//
// primaryEffort is checked per rank rather than once: reasoning-effort legality
// is agent-dependent, so a fallback on a different agent cannot blindly inherit
// the primary's effort.
//
// Duplicates are accepted, matching the code-review roster: ranking the same
// model twice is wasteful, not invalid, and the runtime collapses consecutive
// duplicates anyway.
func (f AutomationFallbackModels) Validate(primaryAgentType *string, primaryEffort *ReasoningEffort) error {
	if len(f.Models) == 0 {
		if len(f.AgentTypes) > 0 || len(f.ReasoningEfforts) > 0 {
			return fmt.Errorf("fallback model agent types and reasoning efforts require a fallback model list")
		}
		return nil
	}
	if len(f.Models) > MaxAutomationFallbackModels {
		return fmt.Errorf("at most %d fallback models are allowed", MaxAutomationFallbackModels)
	}
	if len(f.AgentTypes) > 0 && len(f.AgentTypes) != len(f.Models) {
		return fmt.Errorf("fallback model agent types must match the number of fallback models")
	}
	if len(f.ReasoningEfforts) > 0 && len(f.ReasoningEfforts) != len(f.Models) {
		return fmt.Errorf("fallback model reasoning efforts must match the number of fallback models")
	}

	for idx, model := range f.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			return fmt.Errorf("fallback model %d must be non-empty", idx+1)
		}
		resolvedAgent := f.AgentTypeAt(idx, primaryAgentType)
		if resolvedAgent == nil || strings.TrimSpace(*resolvedAgent) == "" {
			return fmt.Errorf("fallback model %d (%q) is not recognized; set an agent for it", idx+1, model)
		}
		agentType := AgentType(strings.TrimSpace(*resolvedAgent))
		if err := agentType.Validate(); err != nil {
			return fmt.Errorf("invalid agent for fallback model %d: %w", idx+1, err)
		}
		if err := ValidateModelForAgentType(agentType, model); err != nil {
			return fmt.Errorf("invalid fallback model %d: %w", idx+1, err)
		}
		effort := f.ReasoningEffortAt(idx, primaryEffort, resolvedAgent)
		if effort == nil || *effort == "" {
			continue
		}
		if err := (*effort).Validate(); err != nil {
			return fmt.Errorf("invalid reasoning effort for fallback model %d: %w", idx+1, err)
		}
		if !agentType.SupportsReasoningEffortLevel(*effort) {
			return fmt.Errorf("reasoning effort %q is not supported for fallback model %d by agent %q", *effort, idx+1, agentType)
		}
	}
	return nil
}

// ModelRanks materializes the automation's ordered model chain: rank 0 is the
// configured primary, followed by each fallback with its resolved agent and
// reasoning effort. Every consumer — validation, the worker's candidate walk,
// the run snapshot reader and the UI's row labels — reads the chain through
// this one accessor, so the flat list the code-review roster stores directly is
// reproduced here without duplicating the primary in storage.
func (a *Automation) ModelRanks() []AutomationModelRank {
	primary := AutomationModelRank{
		AgentType:       a.AgentType,
		Model:           a.ModelOverride,
		ReasoningEffort: a.ReasoningEffort,
	}
	ranks := make([]AutomationModelRank, 0, 1+a.FallbackModels.Len())
	ranks = append(ranks, primary)
	for idx := range a.FallbackModels.Models {
		model := strings.TrimSpace(a.FallbackModels.Models[idx])
		rankAgent := a.FallbackModels.AgentTypeAt(idx, a.AgentType)
		ranks = append(ranks, AutomationModelRank{
			AgentType:       rankAgent,
			Model:           &model,
			ReasoningEffort: a.FallbackModels.ReasoningEffortAt(idx, a.ReasoningEffort, rankAgent),
			Fallback:        true,
		})
	}
	return ranks
}

// AutomationModelRanksFromConfigSnapshot rebuilds the model chain a run was
// dispatched under. The second return value is false when the snapshot predates
// fallback models (or is absent entirely), in which case the caller should read
// the live automation row — the same "absent key means historical default"
// contract as AutomationPublishPolicyFromConfigSnapshot.
func AutomationModelRanksFromConfigSnapshot(raw json.RawMessage) ([]AutomationModelRank, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var snapshot struct {
		AgentType       *string                   `json:"agent_type"`
		ModelOverride   *string                   `json:"model_override"`
		ReasoningEffort *ReasoningEffort          `json:"reasoning_effort"`
		FallbackModels  *AutomationFallbackModels `json:"fallback_models"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, false, fmt.Errorf("parse automation model ranks snapshot: %w", err)
	}
	if snapshot.FallbackModels == nil {
		return nil, false, nil
	}
	automation := Automation{
		AgentType:       snapshot.AgentType,
		ModelOverride:   snapshot.ModelOverride,
		ReasoningEffort: snapshot.ReasoningEffort,
		FallbackModels:  *snapshot.FallbackModels,
	}
	return automation.ModelRanks(), true, nil
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
	TriggerID          *uuid.UUID                    `db:"trigger_id"            json:"trigger_id,omitempty"`
	Provider           *AutomationEventProvider      `db:"provider"              json:"provider,omitempty"`
	ProviderEventID    *string                       `db:"provider_event_id"     json:"provider_event_id,omitempty"`
	TriggerContext     json.RawMessage               `db:"trigger_context"       json:"trigger_context,omitempty"`
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
	// TriggerTarget and TriggerDetails are compact, typed projections of the
	// GitHub config snapshot populated by run list endpoints. They keep clients
	// from parsing config_snapshot (which is intentionally excluded from the
	// polling payload) and describe the PR being evaluated, not a PR created by
	// the automation session.
	TriggerTarget  *AutomationRunTriggerTarget  `json:"trigger_target,omitempty"`
	TriggerDetails *AutomationRunTriggerDetails `json:"trigger_details,omitempty"`
	// PrimaryAgentType and PrimaryModel are the rank-0 model this run was
	// dispatched under, read from its own frozen config snapshot and populated
	// only by the run list endpoints. A reader compares them against
	// Session.AgentType/ModelOverride to tell whether the run fell back; the
	// live automation row cannot answer that, because editing its model would
	// relabel every historical run.
	PrimaryAgentType *string `json:"primary_agent_type,omitempty"`
	PrimaryModel     *string `json:"primary_model,omitempty"`
}

type AutomationRunTriggerTarget struct {
	Repository        string  `json:"repository"`
	PullRequestNumber int     `json:"pull_request_number"`
	PullRequestURL    string  `json:"pull_request_url"`
	PullRequestTitle  *string `json:"pull_request_title,omitempty"`
	HeadSHA           *string `json:"head_sha,omitempty"`
}

type AutomationRunTriggerDetails struct {
	Event           AutomationGitHubEvent `json:"event"`
	ProviderEventID *string               `json:"provider_event_id,omitempty"`
	EventID         *string               `json:"event_id,omitempty"`
	DedupeGroupID   *string               `json:"dedupe_group_id,omitempty"`
	Actor           *string               `json:"actor,omitempty"`
	ActorType       *string               `json:"actor_type,omitempty"`
	BotTriggered    bool                  `json:"bot_triggered"`
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
	// AgentType and ModelOverride record what this attempt actually ran on.
	// A run whose primary model was unavailable dispatches a fallback rank, so
	// the run row has to show the model that ran rather than the one configured.
	AgentType     *string `json:"agent_type,omitempty"`
	ModelOverride *string `json:"model_override,omitempty"`
	// PR is populated only when a PullRequest row exists for this session.
	// Reuses models.PRSummary so the frontend can share rendering with the
	// session list page.
	PR *PRSummary `json:"pr,omitempty"`
}

type AutomationGoalImprovementMode string

const (
	AutomationGoalImprovementModeFast AutomationGoalImprovementMode = "fast"
	AutomationGoalImprovementModeDeep AutomationGoalImprovementMode = "deep"
)

func (m AutomationGoalImprovementMode) Validate() error {
	switch m {
	case AutomationGoalImprovementModeFast, AutomationGoalImprovementModeDeep:
		return nil
	default:
		return fmt.Errorf("invalid automation goal improvement mode: %q", m)
	}
}

type AutomationGoalImprovementStatus string

const (
	AutomationGoalImprovementStatusPending   AutomationGoalImprovementStatus = "pending"
	AutomationGoalImprovementStatusRunning   AutomationGoalImprovementStatus = "running"
	AutomationGoalImprovementStatusCompleted AutomationGoalImprovementStatus = "completed"
	AutomationGoalImprovementStatusFailed    AutomationGoalImprovementStatus = "failed"
	AutomationGoalImprovementStatusCanceled  AutomationGoalImprovementStatus = "canceled"
)

func (s AutomationGoalImprovementStatus) Validate() error {
	switch s {
	case AutomationGoalImprovementStatusPending,
		AutomationGoalImprovementStatusRunning,
		AutomationGoalImprovementStatusCompleted,
		AutomationGoalImprovementStatusFailed,
		AutomationGoalImprovementStatusCanceled:
		return nil
	default:
		return fmt.Errorf("invalid automation goal improvement status: %q", s)
	}
}

type AutomationGoalImprovement struct {
	ID                uuid.UUID                       `db:"id" json:"id"`
	OrgID             uuid.UUID                       `db:"org_id" json:"org_id"`
	AutomationID      *uuid.UUID                      `db:"automation_id" json:"automation_id,omitempty"`
	RepositoryID      *uuid.UUID                      `db:"repository_id" json:"repository_id,omitempty"`
	Mode              AutomationGoalImprovementMode   `db:"mode" json:"mode"`
	Status            AutomationGoalImprovementStatus `db:"status" json:"status"`
	InputName         *string                         `db:"input_name" json:"input_name,omitempty"`
	InputGoal         string                          `db:"input_goal" json:"input_goal"`
	InputConfig       json.RawMessage                 `db:"input_config" json:"input_config,omitempty"`
	BaseGoalHash      string                          `db:"base_goal_hash" json:"base_goal_hash"`
	EvidenceSnapshot  json.RawMessage                 `db:"evidence_snapshot" json:"evidence_snapshot,omitempty"`
	ProposedGoal      *string                         `db:"proposed_goal" json:"proposed_goal,omitempty"`
	Proposal          json.RawMessage                 `db:"proposal" json:"proposal,omitempty"`
	Confidence        *string                         `db:"confidence" json:"confidence,omitempty"`
	Warnings          json.RawMessage                 `db:"warnings" json:"warnings,omitempty"`
	ErrorMessage      *string                         `db:"error_message" json:"error_message,omitempty"`
	AnalysisSessionID *uuid.UUID                      `db:"analysis_session_id" json:"analysis_session_id,omitempty"`
	CreatedBy         *uuid.UUID                      `db:"created_by" json:"created_by,omitempty"`
	AppliedBy         *uuid.UUID                      `db:"applied_by" json:"applied_by,omitempty"`
	AppliedAt         *time.Time                      `db:"applied_at" json:"applied_at,omitempty"`
	CreatedAt         time.Time                       `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time                       `db:"updated_at" json:"updated_at"`
}

type AutomationGoalImprovementProposal struct {
	Rationale string   `json:"rationale"`
	Changes   []string `json:"changes"`
	Evidence  []string `json:"evidence"`
	Risks     []string `json:"risks"`
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
	AutomationTriggeredBySchedule      AutomationTriggeredBy = "schedule"
	AutomationTriggeredByManual        AutomationTriggeredBy = "manual"
	AutomationTriggeredByGitHub        AutomationTriggeredBy = "github"
	AutomationTriggeredByProviderEvent AutomationTriggeredBy = "provider_event"
)

func (t AutomationTriggeredBy) Validate() error {
	switch t {
	case AutomationTriggeredBySchedule, AutomationTriggeredByManual, AutomationTriggeredByGitHub, AutomationTriggeredByProviderEvent:
		return nil
	default:
		return fmt.Errorf("invalid automation triggered_by: %q", t)
	}
}

type AutomationGitHubEvent string

const (
	AutomationGitHubEventPullRequestOpened  AutomationGitHubEvent = "github.pull_request.opened"
	AutomationGitHubEventPullRequestUpdated AutomationGitHubEvent = "github.pull_request.updated"
	// AutomationGitHubEventPullRequestReadyForReview fires when a pull request
	// becomes reviewable: GitHub's ready_for_review action (draft lifted) and
	// non-draft opened/reopened actions, which GitHub does not follow with a
	// separate ready_for_review delivery.
	AutomationGitHubEventPullRequestReadyForReview       AutomationGitHubEvent = "github.pull_request.ready_for_review"
	AutomationGitHubEventPullRequestMerged               AutomationGitHubEvent = "github.pull_request.merged"
	AutomationGitHubEventCheckSuiteCompleted             AutomationGitHubEvent = "github.check_suite.completed"
	AutomationGitHubEventCheckRunCompleted               AutomationGitHubEvent = "github.check_run.completed"
	AutomationGitHubEventIssueCommentCreated             AutomationGitHubEvent = "github.issue_comment.created"
	AutomationGitHubEventPullRequestReviewSubmitted      AutomationGitHubEvent = "github.pull_request_review.submitted"
	AutomationGitHubEventPullRequestReviewCommentCreated AutomationGitHubEvent = "github.pull_request_review_comment.created"
)

func (e AutomationGitHubEvent) Validate() error {
	switch e {
	case AutomationGitHubEventPullRequestOpened,
		AutomationGitHubEventPullRequestUpdated,
		AutomationGitHubEventPullRequestReadyForReview,
		AutomationGitHubEventPullRequestMerged,
		AutomationGitHubEventCheckSuiteCompleted,
		AutomationGitHubEventCheckRunCompleted,
		AutomationGitHubEventIssueCommentCreated,
		AutomationGitHubEventPullRequestReviewSubmitted,
		AutomationGitHubEventPullRequestReviewCommentCreated:
		return nil
	default:
		return fmt.Errorf("invalid automation github event: %q", e)
	}
}

type AutomationProductTrigger string

const (
	AutomationProductTriggerPROpened         AutomationProductTrigger = "github.pr.opened"
	AutomationProductTriggerPRUpdated        AutomationProductTrigger = "github.pr.updated"
	AutomationProductTriggerPRReadyForReview AutomationProductTrigger = "github.pr.ready_for_review"
	AutomationProductTriggerPRFeedback       AutomationProductTrigger = "github.pr.feedback"
	AutomationProductTriggerChecksCompleted  AutomationProductTrigger = "github.checks.completed"
	AutomationProductTriggerPRMerged         AutomationProductTrigger = "github.pr.merged"
)

func (t AutomationProductTrigger) Validate() error {
	switch t {
	case AutomationProductTriggerPROpened,
		AutomationProductTriggerPRUpdated,
		AutomationProductTriggerPRReadyForReview,
		AutomationProductTriggerPRFeedback,
		AutomationProductTriggerChecksCompleted,
		AutomationProductTriggerPRMerged:
		return nil
	default:
		return fmt.Errorf("invalid automation trigger: %q", t)
	}
}

type AutomationGitHubEventFilters struct {
	BaseBranches []string `json:"base_branches,omitempty"`
	Authors      []string `json:"authors,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	// Labels matches the pull request's GitHub labels. An event passes when the
	// PR carries at least one of the configured labels (case-insensitive).
	// Unlike the other filters this one is strict: an event whose labels could
	// not be determined is filtered out rather than allowed through, so a
	// "frontend"-scoped automation never fires on an unlabelled PR.
	Labels        []string `json:"labels,omitempty"`
	FeedbackTypes []string `json:"feedback_types,omitempty"`
	ReviewStates  []string `json:"review_states,omitempty"`
}

type AutomationScheduleType string

const (
	AutomationScheduleInterval AutomationScheduleType = "interval"
	AutomationScheduleCron     AutomationScheduleType = "cron"
	AutomationScheduleNone     AutomationScheduleType = "none"
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

// AutomationPublishPolicy controls whether a successful automation run should
// automatically open a pull request. Branch-only publication is intentionally
// not supported: review/reporting automations use none, while coding
// automations use pull_request.
type AutomationPublishPolicy string

const (
	AutomationPublishPolicyPullRequest AutomationPublishPolicy = "pull_request"
	AutomationPublishPolicyNone        AutomationPublishPolicy = "none"
)

func (p AutomationPublishPolicy) Validate() error {
	switch p {
	case AutomationPublishPolicyPullRequest, AutomationPublishPolicyNone:
		return nil
	default:
		return fmt.Errorf("invalid publish_policy: %q (must be pull_request or none)", p)
	}
}

func (p AutomationPublishPolicy) OrDefault() AutomationPublishPolicy {
	if p == "" {
		return AutomationPublishPolicyPullRequest
	}
	return p
}

// AutomationPublishPolicyFromConfigSnapshot resolves the policy captured when
// an automation run started. Snapshots created before publish_policy existed
// retain the historical pull-request behavior.
func AutomationPublishPolicyFromConfigSnapshot(raw json.RawMessage) (AutomationPublishPolicy, error) {
	if len(raw) == 0 {
		return AutomationPublishPolicyPullRequest, nil
	}
	var snapshot struct {
		PublishPolicy AutomationPublishPolicy `json:"publish_policy"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return "", fmt.Errorf("parse automation publish policy snapshot: %w", err)
	}
	policy := snapshot.PublishPolicy.OrDefault()
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return policy, nil
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
	var previousRunAt *string
	if a.LastRunAt != nil {
		formatted := a.LastRunAt.UTC().Format(time.RFC3339)
		previousRunAt = &formatted
	}
	data, err := json.Marshal(map[string]any{
		"agent_type":          a.AgentType,
		"model_override":      a.ModelOverride,
		"reasoning_effort":    a.ReasoningEffort,
		"fallback_models":     a.FallbackModels,
		"scope":               a.Scope,
		"identity_scope":      a.IdentityScope.OrDefault(),
		"publish_policy":      a.PublishPolicy.OrDefault(),
		"pre_pr_review_loops": a.PrePRReviewLoops,
		"base_branch":         a.BaseBranch,
		"previous_run_at":     previousRunAt,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal automation config snapshot: %w", err)
	}
	return data, nil
}

func (t AutomationScheduleType) Validate() error {
	switch t {
	case AutomationScheduleInterval, AutomationScheduleCron, AutomationScheduleNone:
		return nil
	default:
		return fmt.Errorf("invalid schedule_type: %q (must be interval, cron, or none)", t)
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
	case AutomationScheduleNone:
		return time.Time{}, nil
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
