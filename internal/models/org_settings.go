package models

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// AutonomyLevel controls when the system auto-triggers agent runs.
type AutonomyLevel string

const (
	AutonomyLevelManual     AutonomyLevel = "manual"
	AutonomyLevelAutoSimple AutonomyLevel = "auto_simple"
	AutonomyLevelAutoAll    AutonomyLevel = "auto_all"
)

// Validate returns an error if the autonomy level is not a recognized value.
func (a AutonomyLevel) Validate() error {
	switch a {
	case AutonomyLevelManual, AutonomyLevelAutoSimple, AutonomyLevelAutoAll:
		return nil
	default:
		return fmt.Errorf("invalid autonomy level: %q", a)
	}
}

// AgentType identifies a coding agent backend.
type AgentType string

const (
	AgentTypeClaudeCode AgentType = "claude_code"
	AgentTypeGeminiCLI  AgentType = "gemini_cli"
	AgentTypeCodex      AgentType = "codex"
	AgentTypeAmp        AgentType = "amp"
	AgentTypePi         AgentType = "pi"
	AgentTypePMAgent    AgentType = "pm_agent"
)

// Validate returns an error if the agent type is not a recognized value.
func (a AgentType) Validate() error {
	switch a {
	case AgentTypeClaudeCode, AgentTypeGeminiCLI, AgentTypeCodex, AgentTypeAmp, AgentTypePi:
		return nil
	default:
		return fmt.Errorf("invalid agent type: %q", a)
	}
}

// AgentEnvConfig holds per-agent environment variable overrides.
// Keys are agent type names (e.g. "claude_code", "gemini_cli", "codex"),
// values are maps of env var name → value.
//
// Injection scope: the orchestrator only injects agent_config.<type>.* into
// the sandbox env for Amp and Pi, and only for their non-secret runtime
// defaults (for example `AMP_MODE` and `PI_MODEL`). For
// claude_code/codex/gemini_cli the legacy behavior stands — provider creds
// come exclusively from resolveProviderConfig and entries here are validated
// and stored, but not injected, so the same key in both places doesn't
// silently override the credential store. See Orchestrator.resolveAgentEnv
// for the exact gating.
type AgentEnvConfig map[string]map[string]string

// ReasoningEffort controls how much reasoning a model should use.
// Valid values: "low", "medium", "high", "xhigh", "max", or "" (default/none).
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortXHigh  ReasoningEffort = "xhigh"
	ReasoningEffortMax    ReasoningEffort = "max"
)

// Validate returns an error if the reasoning effort is not a recognized value.
func (r ReasoningEffort) Validate() error {
	switch r {
	case "", ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh, ReasoningEffortMax:
		return nil
	default:
		return fmt.Errorf("invalid reasoning effort: %q", r)
	}
}

// SupportedReasoningEfforts returns the reasoning levels the agent runtime can
// honor today. The empty value is intentionally omitted; callers should treat
// it as "no explicit override".
func (a AgentType) SupportedReasoningEfforts() []ReasoningEffort {
	switch a {
	case AgentTypeCodex:
		return []ReasoningEffort{
			ReasoningEffortLow,
			ReasoningEffortMedium,
			ReasoningEffortHigh,
			ReasoningEffortXHigh,
		}
	case AgentTypeClaudeCode:
		return []ReasoningEffort{
			ReasoningEffortLow,
			ReasoningEffortMedium,
			ReasoningEffortHigh,
			ReasoningEffortXHigh,
			ReasoningEffortMax,
		}
	default:
		return nil
	}
}

// SupportsReasoningEffort reports whether the agent runtime can honor any
// explicit reasoning effort override.
func (a AgentType) SupportsReasoningEffort() bool {
	return len(a.SupportedReasoningEfforts()) > 0
}

// SupportsReasoningEffortLevel reports whether the given explicit effort is
// supported by the selected coding agent.
func (a AgentType) SupportsReasoningEffortLevel(level ReasoningEffort) bool {
	for _, supported := range a.SupportedReasoningEfforts() {
		if supported == level {
			return true
		}
	}
	return false
}

// OrgSize classifies an organization by volume of issues and activity.
// It drives default context limits to ensure the PM agent gets sufficient
// signal for prioritization without blowing token budgets.
type OrgSize string

const (
	OrgSizeSmall      OrgSize = "small"      // <50 open issues, ≤3 concurrent runs
	OrgSizeMedium     OrgSize = "medium"     // 50–200 open issues, ≤5 concurrent runs
	OrgSizeLarge      OrgSize = "large"      // 200–1000 open issues, ≤10 concurrent runs
	OrgSizeEnterprise OrgSize = "enterprise" // 1000+ open issues, ≤20 concurrent runs
)

// Validate returns an error if the org size is not a recognized value.
func (s OrgSize) Validate() error {
	switch s {
	case OrgSizeSmall, OrgSizeMedium, OrgSizeLarge, OrgSizeEnterprise:
		return nil
	default:
		return fmt.Errorf("invalid org size: %q", s)
	}
}

// ContextLimits controls how much data is gathered for PM and agent contexts.
// All fields are configurable per-org; zero values are replaced with
// size-appropriate defaults in ParseOrgSettings.
type ContextLimits struct {
	// PM context gathering limits
	MaxOpenIssues       int `json:"max_open_issues"`       // max open issues fetched for PM
	MaxTriagedIssues    int `json:"max_triaged_issues"`    // max triaged issues fetched for PM
	MaxInFlightRuns     int `json:"max_in_flight_runs"`    // max in-flight sessions shown to PM
	MaxRecentOutcomes   int `json:"max_recent_outcomes"`   // max recent completed/failed runs
	MaxRecentPRs        int `json:"max_recent_prs"`        // max recent PRs shown to PM
	MaxDecisionHistory  int `json:"max_decision_history"`  // max past decisions shown to PM
	IssueDescriptionMax int `json:"issue_description_max"` // max chars per issue description

	// Token limits for agents
	PMMaxTokens       int `json:"pm_max_tokens"`        // max tokens for PM agent context
	AgentLowTokenMax  int `json:"agent_low_token_max"`  // token limit for low-complexity tasks
	AgentHighTokenMax int `json:"agent_high_token_max"` // token limit for high-complexity tasks
}

// WithDefaults returns a copy of the ContextLimits with any zero-valued fields
// filled in from defaults. This is idempotent — calling it on an already-complete
// ContextLimits returns an identical copy.
func (c ContextLimits) WithDefaults(defaults ContextLimits) ContextLimits {
	out := c
	if out.MaxOpenIssues == 0 {
		out.MaxOpenIssues = defaults.MaxOpenIssues
	}
	if out.MaxTriagedIssues == 0 {
		out.MaxTriagedIssues = defaults.MaxTriagedIssues
	}
	if out.MaxInFlightRuns == 0 {
		out.MaxInFlightRuns = defaults.MaxInFlightRuns
	}
	if out.MaxRecentOutcomes == 0 {
		out.MaxRecentOutcomes = defaults.MaxRecentOutcomes
	}
	if out.MaxRecentPRs == 0 {
		out.MaxRecentPRs = defaults.MaxRecentPRs
	}
	if out.MaxDecisionHistory == 0 {
		out.MaxDecisionHistory = defaults.MaxDecisionHistory
	}
	if out.IssueDescriptionMax == 0 {
		out.IssueDescriptionMax = defaults.IssueDescriptionMax
	}
	if out.PMMaxTokens == 0 {
		out.PMMaxTokens = defaults.PMMaxTokens
	}
	if out.AgentLowTokenMax == 0 {
		out.AgentLowTokenMax = defaults.AgentLowTokenMax
	}
	if out.AgentHighTokenMax == 0 {
		out.AgentHighTokenMax = defaults.AgentHighTokenMax
	}
	return out
}

// PRAuthorship controls who creates the PR on GitHub.
type PRAuthorship string

const (
	// PRAuthorshipUserPreferred uses the user's GitHub token if available, falls back to the app.
	PRAuthorshipUserPreferred PRAuthorship = "user_preferred"
	// PRAuthorshipAppOnly always uses the GitHub App (current/legacy behavior).
	PRAuthorshipAppOnly PRAuthorship = "app_only"
	// PRAuthorshipUserRequired requires user GitHub auth; blocks PR creation if not connected.
	PRAuthorshipUserRequired PRAuthorship = "user_required"
)

// Validate returns an error if the PR authorship mode is not a recognized value.
func (p PRAuthorship) Validate() error {
	switch p {
	case "", PRAuthorshipUserPreferred, PRAuthorshipAppOnly, PRAuthorshipUserRequired:
		return nil
	default:
		return fmt.Errorf("invalid pr_authorship mode: %q", p)
	}
}

// OrgSettings is the strongly-typed representation of organizations.settings JSONB.
type OrgSettings struct {
	AutonomyLevel              AutonomyLevel          `json:"autonomy_level"`
	Aggressiveness             int                    `json:"execution_aggressiveness"`
	MaxConcurrentRuns          int                    `json:"max_concurrent_runs"`
	AgentAutonomy              string                 `json:"agent_autonomy"`
	PriorityWeights            PriorityWeights        `json:"priority_weights"`
	MinPriorityThreshold       float64                `json:"min_priority_threshold"`
	ProductDirection           string                 `json:"product_direction"`
	ProductContext             *ProductContext        `json:"product_context,omitempty"`
	PMScheduleHours            int                    `json:"pm_schedule_hours"`
	PMModel                    string                 `json:"pm_model"`
	LLMModel                   string                 `json:"llm_model"`
	LLMReasoningEffort         ReasoningEffort        `json:"llm_reasoning_effort,omitempty"`
	AgentConfig                AgentEnvConfig         `json:"agent_config,omitempty"`
	DefaultAgentType           AgentType              `json:"default_agent_type,omitempty"`
	AuditRetentionDays         int                    `json:"audit_retention_days,omitempty"`
	ContextRefreshIntervalDays int                    `json:"context_refresh_interval_days,omitempty"`
	OrgSize                    OrgSize                `json:"org_size,omitempty"`
	ContextLimits              ContextLimits          `json:"context_limits,omitempty"`
	PRAuthorship               PRAuthorship           `json:"pr_authorship,omitempty"`
	PRDraftDefault             bool                   `json:"pr_draft_default,omitempty"`
	AutoArchiveOnPRClose       bool                   `json:"auto_archive_on_pr_close,omitempty"`
	BuilderPermissions         BuilderPermissions     `json:"builder_permissions,omitempty"`
	SandboxNetwork             SandboxNetworkSettings `json:"sandbox_network,omitempty"`
	// CodingAgentTabToolsEnabled controls whether sandbox agents may use
	// 143-tools to view/create/message tabs in their current session. Pointer
	// typed so absent settings default on without losing explicit false.
	CodingAgentTabToolsEnabled *bool `json:"coding_agent_tab_tools_enabled,omitempty"`

	// MaxSessionDurationSeconds is the per-session wall-clock timeout applied
	// as the soft runtime budget for run_agent and continue_session jobs.
	// Zero falls back to DefaultMaxSessionDurationSeconds.
	MaxSessionDurationSeconds int `json:"max_session_duration_seconds,omitempty"`

	// PreviewMaxPreviewsPerUser limits how many active previews one user can
	// keep running in this organization. Zero falls back to
	// DefaultPreviewMaxPreviewsPerUser.
	PreviewMaxPreviewsPerUser int `json:"preview_max_previews_per_user,omitempty"`

	RuntimeBudgets RuntimeBudgetSettings `json:"runtime_budgets,omitempty"`

	SandboxLifecycle SandboxLifecycleSettings `json:"sandbox_lifecycle,omitempty"`
	SandboxResources SandboxResourceSettings  `json:"sandbox_resources,omitempty"`

	// LinearAutomation controls how 143 reflects session events back into
	// Linear. The two write levels are intentionally separate so teams can
	// adopt visibility (attachment + rolling comment) before they are ready
	// for state automation. See design 62 §"Per-org configuration UI".
	LinearAutomation LinearAutomationSettings `json:"linear_automation,omitempty"`

	// LinearAgent controls the inbound agent feature: assign / @-mention the
	// @143 user in Linear and a coding session is created. Distinct from
	// LinearAutomation, which is purely outbound. The feature is opt-in;
	// nothing happens to a Linear-connected org until an admin enables it.
	LinearAgent LinearAgentSettings `json:"linear_agent,omitempty"`
}

// SandboxNetworkSettings controls per-org sandbox egress behavior.
type SandboxNetworkSettings struct {
	StaticEgressEnabled bool `json:"static_egress_enabled,omitempty"`
}

// SandboxLifecycleSettings controls cleanup and retention defaults for runtime artifacts.
type SandboxLifecycleSettings struct {
	CompletedSessionRetentionMinutes int   `json:"completed_session_retention_minutes,omitempty"`
	IdlePreviewTTLMinutes            int   `json:"idle_preview_ttl_minutes,omitempty"`
	PreviewHoldsSandbox              *bool `json:"preview_holds_sandbox,omitempty"`
}

// EffectivePreviewHoldsSandbox applies the default preview hold policy.
func (s SandboxLifecycleSettings) EffectivePreviewHoldsSandbox() bool {
	if s.PreviewHoldsSandbox == nil {
		return true
	}
	return *s.PreviewHoldsSandbox
}

// SandboxResourceTier is a named resource-size tier for sandbox runtime defaults.
type SandboxResourceTier string

const (
	SandboxResourceTierSmall    SandboxResourceTier = "small"
	SandboxResourceTierStandard SandboxResourceTier = "standard"
	SandboxResourceTierLarge    SandboxResourceTier = "large"
)

// Validate returns an error when the tier is not one of the supported values.
func (t SandboxResourceTier) Validate() error {
	switch t {
	case "", SandboxResourceTierSmall, SandboxResourceTierStandard, SandboxResourceTierLarge:
		return nil
	default:
		return fmt.Errorf("invalid sandbox resource tier: %q", t)
	}
}

// SandboxResourceSettings controls org-level sandbox resource defaults and bounds.
type SandboxResourceSettings struct {
	AgentDefaultTier          SandboxResourceTier `json:"agent_default_tier,omitempty"`
	PreviewDefaultTier        SandboxResourceTier `json:"preview_default_tier,omitempty"`
	AllowRepoResourceRequests *bool               `json:"allow_repo_resource_requests,omitempty"`
	PreviewMaxTier            SandboxResourceTier `json:"preview_max_tier,omitempty"`
}

// EffectiveAllowRepoResourceRequests applies the default resource policy.
func (s SandboxResourceSettings) EffectiveAllowRepoResourceRequests() bool {
	if s.AllowRepoResourceRequests == nil {
		return true
	}
	return *s.AllowRepoResourceRequests
}

func (s OrgSettings) EffectiveCodingAgentTabToolsEnabled() bool {
	if s.CodingAgentTabToolsEnabled == nil {
		return true
	}
	return *s.CodingAgentTabToolsEnabled
}

// BuilderPermissions controls the narrower builder role's access to
// publishing actions. Pointer fields preserve absent-vs-explicit-false.
type BuilderPermissions struct {
	RequireReviewBeforePR *bool `json:"require_review_before_pr,omitempty"`
}

// EffectiveRequireReviewBeforePR applies the default builder guardrail.
func (p BuilderPermissions) EffectiveRequireReviewBeforePR() bool {
	if p.RequireReviewBeforePR == nil {
		return true
	}
	return *p.RequireReviewBeforePR
}

// LinearAutomationSettings captures org-level defaults plus per-team
// overrides for Linear write-back behavior.
//
// Pointer-typed flags distinguish "field absent in JSON" (nil → apply
// design default) from "explicitly set to false" (non-nil → respect the
// user). Without pointers we'd be unable to tell a fresh org from one
// that explicitly turned automation off, and would silently re-enable
// the writes on every settings reload.
type LinearAutomationSettings struct {
	// PostSessionLinks enables attachment + rolling-comment writes. Defaults
	// to true when nil. Many teams will want this before they enable state
	// automation.
	PostSessionLinks *bool `json:"post_session_links,omitempty"`
	// MoveWorkflowStates enables forward-only state transitions on the
	// primary linked Linear issue. Defaults to true when nil; teams that
	// use Linear as a strict daily Kanban with manual moves can disable it.
	MoveWorkflowStates *bool `json:"move_workflow_states,omitempty"`
	// ReviewStateNamePreferences lists the workflow state names we'd prefer
	// to land in when a PR opens, in order. First match in the team's state
	// set wins. Stored as raw strings because workspaces customize freely.
	//
	// Semantics: empty slice and nil are treated identically — both fall
	// back to DefaultLinearReviewStateNames. There is no "explicitly empty
	// to disable PR-open transitions" knob; use MoveWorkflowStates=false
	// for that. If we ever need to express "leave PR-open state alone but
	// keep the other transitions," switch this field to *[]string so the
	// nil-vs-empty distinction becomes meaningful.
	ReviewStateNamePreferences []string `json:"review_state_name_preferences,omitempty"`
	// PerTeam overrides keyed by Linear team key. Inherits the org-level
	// flags when the key is missing.
	PerTeam map[string]LinearTeamAutomationOverride `json:"per_team,omitempty"`
	// AllowPerSessionOverrides controls whether non-admin users may set
	// linear_private and linear_state_sync_disabled on individual sessions.
	// Defaults to true (current behavior) so the flags stay user-controllable
	// out of the box. Admins enforcing "every session must sync to Linear"
	// can flip this to false; the API rejects any session-create request
	// that carries those flags. Pointer-typed for the same nil-vs-explicit
	// reason as the rest of this struct.
	AllowPerSessionOverrides *bool `json:"allow_per_session_overrides,omitempty"`
}

// EffectiveAllowPerSessionOverrides resolves the org-level
// allow-per-session-overrides flag, applying the design default (true)
// when the JSON had no explicit value. False means the API must reject
// session creates that try to set linear_private or
// linear_state_sync_disabled.
func (s LinearAutomationSettings) EffectiveAllowPerSessionOverrides() bool {
	if s.AllowPerSessionOverrides == nil {
		return true
	}
	return *s.AllowPerSessionOverrides
}

// EffectivePostSessionLinks resolves the org-level post-session-links flag,
// applying the design default (true) when the JSON had no explicit value.
func (s LinearAutomationSettings) EffectivePostSessionLinks() bool {
	if s.PostSessionLinks == nil {
		return true
	}
	return *s.PostSessionLinks
}

// EffectiveMoveWorkflowStates resolves the org-level move-workflow-states
// flag, applying the design default (true) when no explicit value was set.
func (s LinearAutomationSettings) EffectiveMoveWorkflowStates() bool {
	if s.MoveWorkflowStates == nil {
		return true
	}
	return *s.MoveWorkflowStates
}

// LinearTeamAutomationOverride is a per-team override of the org defaults.
// Pointers are used so we can distinguish "explicitly off" from "inherit".
type LinearTeamAutomationOverride struct {
	PostSessionLinks   *bool `json:"post_session_links,omitempty"`
	MoveWorkflowStates *bool `json:"move_workflow_states,omitempty"`
}

// PostSessionLinksFor resolves the effective post-session-links flag for the
// given team key, applying per-team override on top of org defaults.
func (s LinearAutomationSettings) PostSessionLinksFor(teamKey string) bool {
	if override, ok := s.PerTeam[teamKey]; ok && override.PostSessionLinks != nil {
		return *override.PostSessionLinks
	}
	return s.EffectivePostSessionLinks()
}

// MoveWorkflowStatesFor resolves the effective move-workflow-states flag.
func (s LinearAutomationSettings) MoveWorkflowStatesFor(teamKey string) bool {
	if override, ok := s.PerTeam[teamKey]; ok && override.MoveWorkflowStates != nil {
		return *override.MoveWorkflowStates
	}
	return s.EffectiveMoveWorkflowStates()
}

// DefaultLinearReviewStateNames is the fallback list when an org hasn't set
// its own preferences. The order reflects what most workspaces use.
var DefaultLinearReviewStateNames = []string{
	"In Review", "Code Review", "PR Open", "Pull Request Open",
}

// LinearAgentSettings controls the inbound Linear agent feature: assign /
// @-mention the @143 user and a 143 session is created. Pointer-typed
// flags follow the same nil-vs-explicit convention as LinearAutomationSettings
// — nil means "apply design default", non-nil means "respect the user".
//
// The feature is opt-in by default. An org can have Linear connected for
// outbound write-back without enabling the agent for inbound triggering;
// the two are independent.
type LinearAgentSettings struct {
	// Enabled gates the entire feature. nil/false means "ignore Linear
	// AgentSessionEvent webhooks for this org" — the dispatcher records the
	// delivery for audit and returns 200 immediately without doing any work.
	// True means the dispatcher will resolve a repo and kick off a session.
	//
	// Pointer so a future migration that flips the default doesn't silently
	// retro-enable orgs that explicitly turned the feature off.
	Enabled *bool `json:"enabled,omitempty"`
	// DefaultRepoID is the org-wide fallback repo for AgentSessions whose
	// Linear team has no entry in linear_team_repo_mappings. Empty means
	// "no fallback" — the dispatcher emits a `response` activity asking the
	// admin to configure a mapping and closes the AgentSession with
	// state=complete (an actionable user message, not an error).
	DefaultRepoID *uuid.UUID `json:"default_repo_id,omitempty"`
	// AppUserHandle is the @-handle the @143 agent user goes by in Linear.
	// Cosmetic — only used for UI copy. Defaults to "143".
	AppUserHandle string `json:"app_user_handle,omitempty"`
	// AllowRevisionPerPrompt controls whether `prompted` events on a 143
	// session that has reached terminal state spawn a revision session
	// (true) or are quietly ignored with a `response` activity explaining
	// the prior session has ended (false). Default true; the explicit knob
	// exists so heavy users can opt out of the implicit "every late comment
	// reopens the work" behavior.
	AllowRevisionPerPrompt *bool `json:"allow_revision_per_prompt,omitempty"`
	// PerTeamEnabled overrides the org-level Enabled flag at team granularity.
	// Keyed by Linear team key (e.g. "ACS"). Missing key → inherit
	// org-level Enabled. nil entry → inherit. Non-nil entry → respect.
	PerTeamEnabled map[string]*bool `json:"per_team_enabled,omitempty"`
	// AllowLabelRepoOverride lets any Linear member with label-write access
	// redirect an AgentSession to a different repo via a
	// `repo:<full-name>` label on the issue. Opt-in (default false) because
	// it weakens the org's "Linear team → repo" routing guarantees:
	// without this flag, repo selection follows the configured
	// linear_team_repo_mappings (admin-controlled) and the org default;
	// with it, any Linear contributor who can apply labels can route work
	// to any repo the org owns. Orgs whose Linear membership equals their
	// 143 admin set can safely enable it.
	AllowLabelRepoOverride *bool `json:"allow_label_repo_override,omitempty"`
}

// EffectiveAllowLabelRepoOverride resolves the org-level
// allow-label-repo-override flag, applying the design default (false —
// opt-in) when no explicit value was set.
func (s LinearAgentSettings) EffectiveAllowLabelRepoOverride() bool {
	if s.AllowLabelRepoOverride == nil {
		return false
	}
	return *s.AllowLabelRepoOverride
}

// EffectiveEnabled resolves the org-level Enabled flag, applying the design
// default (false — opt-in) when no explicit value was set.
func (s LinearAgentSettings) EffectiveEnabled() bool {
	if s.Enabled == nil {
		return false
	}
	return *s.Enabled
}

// EnabledFor resolves the effective Enabled flag for the given Linear team
// key, falling back to the org default when the team has no override.
func (s LinearAgentSettings) EnabledFor(teamKey string) bool {
	if s.PerTeamEnabled != nil {
		if override, ok := s.PerTeamEnabled[teamKey]; ok && override != nil {
			return *override
		}
	}
	return s.EffectiveEnabled()
}

// EffectiveAllowRevisionPerPrompt resolves the org-level
// allow-revision-per-prompt flag, applying the design default (true) when
// no explicit value was set.
func (s LinearAgentSettings) EffectiveAllowRevisionPerPrompt() bool {
	if s.AllowRevisionPerPrompt == nil {
		return true
	}
	return *s.AllowRevisionPerPrompt
}

// EffectiveAppUserHandle returns the configured agent user handle, falling
// back to "143" when unset.
func (s LinearAgentSettings) EffectiveAppUserHandle() string {
	if s.AppUserHandle == "" {
		return "143"
	}
	return s.AppUserHandle
}

// Agent autonomy mode constants.
const (
	AgentAutonomyConservative = "conservative"
	AgentAutonomyBalanced     = "balanced"
	AgentAutonomyAggressive   = "aggressive"
)

// PriorityWeights controls how priority scores are computed.
type PriorityWeights struct {
	CustomerImpact float64 `json:"customer_impact"`
	Severity       float64 `json:"severity"`
	Recency        float64 `json:"recency"`
	RevenueRisk    float64 `json:"revenue_risk"`
}

// Default values for org settings.
const (
	DefaultAutonomyLevel              AutonomyLevel = AutonomyLevelAutoSimple
	DefaultAggressiveness                           = 5
	DefaultMaxConcurrentRuns                        = 10
	DefaultAgentAutonomy                            = AgentAutonomyAggressive
	DefaultMinPriorityThreshold                     = 30.0
	DefaultDefaultAgentType           AgentType     = AgentTypeCodex
	DefaultPMScheduleHours                          = 24
	DefaultPMModel                                  = CodexModelGPT54
	DefaultAuditRetentionDays                       = 90
	DefaultContextRefreshIntervalDays               = 14
	DefaultOrgSize                    OrgSize       = OrgSizeMedium

	DefaultWeightCustomerImpact = 0.35
	DefaultWeightSeverity       = 0.25
	DefaultWeightRecency        = 0.20
	DefaultWeightRevenueRisk    = 0.20

	// DefaultMaxSessionDurationSeconds is the default per-session wall-clock
	// timeout (25 minutes). Long enough for non-trivial agent runs, short
	// enough that stuck sessions don't silently eat capacity.
	DefaultMaxSessionDurationSeconds = 25 * 60

	// MinMaxSessionDurationSeconds is the smallest sensible per-org timeout.
	// Values below this produce very short runs that are unlikely to complete
	// a useful agent task; we clamp up to this floor.
	MinMaxSessionDurationSeconds = 2 * 60

	// MaxMaxSessionDurationSeconds is the upper bound for per-org timeout.
	// Values above this are clamped down to protect shared infrastructure
	// (long-running sandboxes hold concurrency slots). Admins wanting longer
	// runs should split the task rather than raising this.
	//
	// If you bump this, also review agent.minRunningAgeFloor (internal/
	// services/agent/reaper.go) and the SESSION_MAX_RUNNING_AGE default in
	// internal/config/config.go — both are derived from this value and must
	// stay strictly above it so the reaper doesn't kill legitimate
	// long-running sessions before the orchestrator's own timeout fires.
	MaxMaxSessionDurationSeconds = 2 * 60 * 60
	// MaxAbsoluteRuntimeCeilingSeconds is the largest valid absolute runtime
	// ceiling after automatic extensions. The worker watchdog adds its own
	// cleanup buffer on top of this, so org settings must not promise a longer
	// handler runtime than workers will keep renewing.
	MaxAbsoluteRuntimeCeilingSeconds = MaxMaxSessionDurationSeconds + 15*60

	DefaultNoProgressTimeoutSeconds        = 15 * 60
	DefaultGracefulShutdownWindowSeconds   = 30
	DefaultCheckpointFinalizeWindowSeconds = 30
	DefaultAutomaticExtensionSeconds       = 10 * 60
	DefaultMaxAutomaticExtensionSeconds    = 30 * 60
	DefaultAbsoluteRuntimeCeilingSeconds   = 90 * 60

	DefaultPreviewMaxPreviewsPerUser = 4
	MinPreviewMaxPreviewsPerUser     = 1
	MaxPreviewMaxPreviewsPerUser     = 20

	DefaultCompletedSessionRetentionMinutes = 60
	MinCompletedSessionRetentionMinutes     = 0
	MaxCompletedSessionRetentionMinutes     = 24 * 60
	DefaultIdlePreviewTTLMinutes            = 4 * 60
	MinIdlePreviewTTLMinutes                = 15
	MaxIdlePreviewTTLMinutes                = 24 * 60
)

// ContextLimits returns the default context limits for this org size.
// These presets balance signal quality against token costs:
//   - Small orgs: minimal context, all issues fit easily
//   - Medium orgs: moderate context (current defaults)
//   - Large orgs: expanded context, more frequent PM runs, higher token budgets
//   - Enterprise orgs: maximum context for comprehensive prioritization
func (s OrgSize) ContextLimits() ContextLimits {
	switch s {
	case OrgSizeSmall:
		return ContextLimits{
			MaxOpenIssues:       50,
			MaxTriagedIssues:    50,
			MaxInFlightRuns:     20,
			MaxRecentOutcomes:   10,
			MaxRecentPRs:        10,
			MaxDecisionHistory:  25,
			IssueDescriptionMax: 500,
			PMMaxTokens:         30_000,
			AgentLowTokenMax:    50_000,
			AgentHighTokenMax:   200_000,
		}
	case OrgSizeLarge:
		return ContextLimits{
			MaxOpenIssues:       300,
			MaxTriagedIssues:    200,
			MaxInFlightRuns:     100,
			MaxRecentOutcomes:   50,
			MaxRecentPRs:        40,
			MaxDecisionHistory:  100,
			IssueDescriptionMax: 400,
			PMMaxTokens:         100_000,
			AgentLowTokenMax:    50_000,
			AgentHighTokenMax:   200_000,
		}
	case OrgSizeEnterprise:
		return ContextLimits{
			MaxOpenIssues:       500,
			MaxTriagedIssues:    300,
			MaxInFlightRuns:     150,
			MaxRecentOutcomes:   75,
			MaxRecentPRs:        60,
			MaxDecisionHistory:  150,
			IssueDescriptionMax: 300,
			PMMaxTokens:         150_000,
			AgentLowTokenMax:    75_000,
			AgentHighTokenMax:   250_000,
		}
	default: // medium (current defaults)
		return ContextLimits{
			MaxOpenIssues:       100,
			MaxTriagedIssues:    100,
			MaxInFlightRuns:     50,
			MaxRecentOutcomes:   20,
			MaxRecentPRs:        20,
			MaxDecisionHistory:  50,
			IssueDescriptionMax: 500,
			PMMaxTokens:         50_000,
			AgentLowTokenMax:    50_000,
			AgentHighTokenMax:   200_000,
		}
	}
}

// PMScheduleHours returns the recommended PM schedule interval for this org size.
// Larger orgs benefit from more frequent PM runs to keep up with higher issue volume.
func (s OrgSize) PMScheduleHours() int {
	switch s {
	case OrgSizeSmall:
		return 6
	case OrgSizeLarge:
		return 2
	case OrgSizeEnterprise:
		return 1
	default: // medium
		return 24
	}
}

// MaxConcurrentRuns returns the recommended concurrency limit for this org size.
func (s OrgSize) MaxConcurrentRuns() int {
	switch s {
	case OrgSizeSmall:
		return 5
	case OrgSizeLarge:
		return 15
	case OrgSizeEnterprise:
		return 25
	default: // medium
		return 10
	}
}

// ProductContext captures the strategic context for the PM agent.
type ProductContext struct {
	Philosophy string   `json:"philosophy"`
	Direction  string   `json:"direction"`
	FocusAreas []string `json:"focus_areas,omitempty"`
	AvoidAreas []string `json:"avoid_areas,omitempty"`
}

// ParseOrgSettings deserializes the JSONB settings column into OrgSettings,
// applying defaults for any missing or zero-valued fields.
func ParseOrgSettings(raw json.RawMessage) (OrgSettings, error) {
	var s OrgSettings
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("unmarshal org settings: %w", err)
		}
	}

	if s.AutonomyLevel == "" {
		s.AutonomyLevel = DefaultAutonomyLevel
	}
	if s.Aggressiveness == 0 {
		s.Aggressiveness = DefaultAggressiveness
	}
	// Resolve org size early so size-aware defaults can be applied below.
	effectiveSize := s.OrgSize
	if effectiveSize == "" {
		effectiveSize = DefaultOrgSize
	}

	if s.MaxConcurrentRuns == 0 {
		s.MaxConcurrentRuns = effectiveSize.MaxConcurrentRuns()
	}
	if s.AgentAutonomy == "" {
		s.AgentAutonomy = DefaultAgentAutonomy
	}
	if s.MinPriorityThreshold == 0 {
		s.MinPriorityThreshold = DefaultMinPriorityThreshold
	}
	if s.PMScheduleHours == 0 {
		s.PMScheduleHours = effectiveSize.PMScheduleHours()
	}
	if s.PMModel == "" {
		s.PMModel = DefaultPMModel
	}
	if s.PriorityWeights == (PriorityWeights{}) {
		s.PriorityWeights = PriorityWeights{
			CustomerImpact: DefaultWeightCustomerImpact,
			Severity:       DefaultWeightSeverity,
			Recency:        DefaultWeightRecency,
			RevenueRisk:    DefaultWeightRevenueRisk,
		}
	}
	if s.DefaultAgentType == "" {
		s.DefaultAgentType = DefaultDefaultAgentType
	}
	if s.AuditRetentionDays == 0 {
		s.AuditRetentionDays = DefaultAuditRetentionDays
	}
	if s.ContextRefreshIntervalDays == 0 {
		s.ContextRefreshIntervalDays = DefaultContextRefreshIntervalDays
	}
	if s.ProductContext == nil && s.ProductDirection != "" {
		s.ProductContext = &ProductContext{
			Direction: s.ProductDirection,
		}
	}

	// Apply org-size-aware defaults for context limits.
	s.ContextLimits = s.ContextLimits.WithDefaults(effectiveSize.ContextLimits())

	// PR authorship: default to user_preferred (zero-value treated as user_preferred).
	if s.PRAuthorship == "" {
		s.PRAuthorship = PRAuthorshipUserPreferred
	}

	// Session duration: default when unset, clamp to [min, max].
	if s.MaxSessionDurationSeconds <= 0 {
		s.MaxSessionDurationSeconds = DefaultMaxSessionDurationSeconds
	} else if s.MaxSessionDurationSeconds < MinMaxSessionDurationSeconds {
		s.MaxSessionDurationSeconds = MinMaxSessionDurationSeconds
	} else if s.MaxSessionDurationSeconds > MaxMaxSessionDurationSeconds {
		s.MaxSessionDurationSeconds = MaxMaxSessionDurationSeconds
	}
	if s.PreviewMaxPreviewsPerUser == 0 {
		s.PreviewMaxPreviewsPerUser = DefaultPreviewMaxPreviewsPerUser
	} else if s.PreviewMaxPreviewsPerUser < MinPreviewMaxPreviewsPerUser {
		s.PreviewMaxPreviewsPerUser = MinPreviewMaxPreviewsPerUser
	} else if s.PreviewMaxPreviewsPerUser > MaxPreviewMaxPreviewsPerUser {
		s.PreviewMaxPreviewsPerUser = MaxPreviewMaxPreviewsPerUser
	}
	if s.SandboxLifecycle.CompletedSessionRetentionMinutes == 0 {
		s.SandboxLifecycle.CompletedSessionRetentionMinutes = DefaultCompletedSessionRetentionMinutes
	} else if s.SandboxLifecycle.CompletedSessionRetentionMinutes < MinCompletedSessionRetentionMinutes {
		s.SandboxLifecycle.CompletedSessionRetentionMinutes = MinCompletedSessionRetentionMinutes
	} else if s.SandboxLifecycle.CompletedSessionRetentionMinutes > MaxCompletedSessionRetentionMinutes {
		s.SandboxLifecycle.CompletedSessionRetentionMinutes = MaxCompletedSessionRetentionMinutes
	}
	if s.SandboxLifecycle.IdlePreviewTTLMinutes == 0 {
		s.SandboxLifecycle.IdlePreviewTTLMinutes = DefaultIdlePreviewTTLMinutes
	} else if s.SandboxLifecycle.IdlePreviewTTLMinutes < MinIdlePreviewTTLMinutes {
		s.SandboxLifecycle.IdlePreviewTTLMinutes = MinIdlePreviewTTLMinutes
	} else if s.SandboxLifecycle.IdlePreviewTTLMinutes > MaxIdlePreviewTTLMinutes {
		s.SandboxLifecycle.IdlePreviewTTLMinutes = MaxIdlePreviewTTLMinutes
	}
	if s.SandboxResources.AgentDefaultTier == "" {
		s.SandboxResources.AgentDefaultTier = SandboxResourceTierStandard
	}
	if s.SandboxResources.PreviewDefaultTier == "" {
		s.SandboxResources.PreviewDefaultTier = SandboxResourceTierStandard
	}
	if s.SandboxResources.PreviewMaxTier == "" {
		s.SandboxResources.PreviewMaxTier = SandboxResourceTierLarge
	}

	if s.RuntimeBudgets.NoProgressTimeoutSeconds <= 0 {
		s.RuntimeBudgets.NoProgressTimeoutSeconds = DefaultNoProgressTimeoutSeconds
	}
	if s.RuntimeBudgets.GracefulShutdownWindowSeconds <= 0 {
		s.RuntimeBudgets.GracefulShutdownWindowSeconds = DefaultGracefulShutdownWindowSeconds
	}
	if s.RuntimeBudgets.CheckpointFinalizationWindowSeconds <= 0 {
		s.RuntimeBudgets.CheckpointFinalizationWindowSeconds = DefaultCheckpointFinalizeWindowSeconds
	}
	if s.RuntimeBudgets.AutomaticExtensionSeconds <= 0 {
		s.RuntimeBudgets.AutomaticExtensionSeconds = DefaultAutomaticExtensionSeconds
	}
	if s.RuntimeBudgets.MaxAutomaticExtensionSeconds < 0 {
		s.RuntimeBudgets.MaxAutomaticExtensionSeconds = 0
	} else if s.RuntimeBudgets.MaxAutomaticExtensionSeconds == 0 {
		s.RuntimeBudgets.MaxAutomaticExtensionSeconds = DefaultMaxAutomaticExtensionSeconds
	}
	if s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds <= 0 {
		s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds = DefaultAbsoluteRuntimeCeilingSeconds
	}
	if s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds < s.MaxSessionDurationSeconds {
		s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds = s.MaxSessionDurationSeconds
	} else if s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds > MaxAbsoluteRuntimeCeilingSeconds {
		s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds = MaxAbsoluteRuntimeCeilingSeconds
	}

	// Linear automation defaults: ensure ReviewStateNamePreferences has a
	// usable list so PR-open transitions resolve when the org never set
	// preferences. Flag defaults (PostSessionLinks=true,
	// MoveWorkflowStates=true) are surfaced via Effective*() accessors so
	// nil pointers stay distinguishable from explicit false at every call
	// site — see LinearAutomationSettings doc.
	if len(s.LinearAutomation.ReviewStateNamePreferences) == 0 {
		s.LinearAutomation.ReviewStateNamePreferences = DefaultLinearReviewStateNames
	}
	if s.RuntimeBudgets.MaxAutomaticExtensionSeconds > 0 {
		maxExtensionByCeiling := s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds - s.MaxSessionDurationSeconds
		if maxExtensionByCeiling < 0 {
			maxExtensionByCeiling = 0
		}
		if s.RuntimeBudgets.MaxAutomaticExtensionSeconds > maxExtensionByCeiling {
			s.RuntimeBudgets.MaxAutomaticExtensionSeconds = maxExtensionByCeiling
		}
	}

	return s, nil
}
