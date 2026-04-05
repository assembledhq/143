package models

import (
	"encoding/json"
	"fmt"
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
	AgentTypePMAgent    AgentType = "pm_agent"
)

// Validate returns an error if the agent type is not a recognized value.
func (a AgentType) Validate() error {
	switch a {
	case AgentTypeClaudeCode, AgentTypeGeminiCLI, AgentTypeCodex:
		return nil
	default:
		return fmt.Errorf("invalid agent type: %q", a)
	}
}

// AgentEnvConfig holds per-agent environment variable overrides.
// Keys are agent type names (e.g. "claude_code", "gemini_cli", "codex"),
// values are maps of env var name → value.
type AgentEnvConfig map[string]map[string]string

// ReasoningEffort controls how much reasoning a model should use.
// Valid values: "low", "medium", "high", or "" (default/none).
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
)

// Validate returns an error if the reasoning effort is not a recognized value.
func (r ReasoningEffort) Validate() error {
	switch r {
	case "", ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh:
		return nil
	default:
		return fmt.Errorf("invalid reasoning effort: %q", r)
	}
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
	PMMaxTokens        int `json:"pm_max_tokens"`         // max tokens for PM agent context
	AgentLowTokenMax   int `json:"agent_low_token_max"`   // token limit for low-complexity tasks
	AgentHighTokenMax  int `json:"agent_high_token_max"`  // token limit for high-complexity tasks
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
	AutonomyLevel        AutonomyLevel        `json:"autonomy_level"`
	Aggressiveness       int                  `json:"execution_aggressiveness"`
	MaxConcurrentRuns    int                  `json:"max_concurrent_runs"`
	AgentAutonomy        string               `json:"agent_autonomy"`
	ConfidenceThresholds ConfidenceThresholds `json:"confidence_thresholds"`
	PriorityWeights      PriorityWeights      `json:"priority_weights"`
	MinPriorityThreshold float64              `json:"min_priority_threshold"`
	ProductDirection     string               `json:"product_direction"`
	ProductContext       *ProductContext      `json:"product_context,omitempty"`
	PMScheduleHours      int                  `json:"pm_schedule_hours"`
	PMModel              string               `json:"pm_model"`
	LLMModel             string               `json:"llm_model"`
	LLMReasoningEffort         ReasoningEffort `json:"llm_reasoning_effort,omitempty"`
	AgentConfig                AgentEnvConfig  `json:"agent_config,omitempty"`
	DefaultAgentType           AgentType       `json:"default_agent_type,omitempty"`
	AuditRetentionDays         int             `json:"audit_retention_days,omitempty"`
	ContextRefreshIntervalDays int             `json:"context_refresh_interval_days,omitempty"`
	OrgSize                    OrgSize         `json:"org_size,omitempty"`
	ContextLimits              ContextLimits   `json:"context_limits,omitempty"`
	PRAuthorship               PRAuthorship    `json:"pr_authorship,omitempty"`
	PRDraftDefault             bool            `json:"pr_draft_default,omitempty"`
}

// Agent autonomy mode constants.
const (
	AgentAutonomyConservative = "conservative"
	AgentAutonomyBalanced     = "balanced"
	AgentAutonomyAggressive   = "aggressive"
)

// ConfidenceThresholdsForAutonomy returns the confidence thresholds that
// correspond to the given agent autonomy mode.
func ConfidenceThresholdsForAutonomy(mode string) ConfidenceThresholds {
	switch mode {
	case AgentAutonomyConservative:
		return ConfidenceThresholds{AutoProceed: 1.0, HumanReview: 0.8}
	case AgentAutonomyAggressive:
		return ConfidenceThresholds{AutoProceed: 0.4, HumanReview: 0.2}
	default: // balanced
		return ConfidenceThresholds{AutoProceed: 0.85, HumanReview: 0.5}
	}
}

// ConfidenceThresholds controls when to auto-proceed vs request human review.
type ConfidenceThresholds struct {
	AutoProceed float64 `json:"auto_proceed"`
	HumanReview float64 `json:"human_review"`
}

// PriorityWeights controls how priority scores are computed.
type PriorityWeights struct {
	CustomerImpact float64 `json:"customer_impact"`
	Severity       float64 `json:"severity"`
	Recency        float64 `json:"recency"`
	RevenueRisk    float64 `json:"revenue_risk"`
}

// Default values for org settings.
const (
	DefaultAutonomyLevel        AutonomyLevel = AutonomyLevelAutoSimple
	DefaultAggressiveness                     = 5
	DefaultMaxConcurrentRuns                  = 10
	DefaultAgentAutonomy                      = AgentAutonomyAggressive
	DefaultMinPriorityThreshold               = 30.0
	DefaultDefaultAgentType     AgentType     = AgentTypeCodex
	DefaultPMScheduleHours                    = 4
	DefaultPMModel                            = PMModelSonnet
	DefaultAuditRetentionDays                 = 90
	DefaultContextRefreshIntervalDays         = 14
	DefaultOrgSize                    OrgSize = OrgSizeMedium

	DefaultWeightCustomerImpact = 0.35
	DefaultWeightSeverity       = 0.25
	DefaultWeightRecency        = 0.20
	DefaultWeightRevenueRisk    = 0.20

	DefaultConfidenceAutoProceed = 0.85
	DefaultConfidenceHumanReview = 0.60
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
		return 4
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
	// Derive confidence thresholds from autonomy mode.
	s.ConfidenceThresholds = ConfidenceThresholdsForAutonomy(s.AgentAutonomy)
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

	return s, nil
}
