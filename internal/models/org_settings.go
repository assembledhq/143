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
	AgentConfig          AgentEnvConfig       `json:"agent_config,omitempty"`
	DefaultAgentType     AgentType            `json:"default_agent_type,omitempty"`
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
	DefaultMaxConcurrentRuns                  = 5
	DefaultAgentAutonomy                      = AgentAutonomyAggressive
	DefaultMinPriorityThreshold               = 30.0
	DefaultDefaultAgentType     AgentType     = AgentTypeCodex
	DefaultPMScheduleHours      = 4
	DefaultPMModel              = PMModelSonnet

	DefaultWeightCustomerImpact = 0.35
	DefaultWeightSeverity       = 0.25
	DefaultWeightRecency        = 0.20
	DefaultWeightRevenueRisk    = 0.20

	DefaultConfidenceAutoProceed = 0.85
	DefaultConfidenceHumanReview = 0.60
)

// ProductContext captures the strategic context for the PM agent.
type ProductContext struct {
	Philosophy string   `json:"philosophy"`
	Direction  string   `json:"direction"`
	FocusAreas []string `json:"focus_areas,omitempty"`
	AvoidAreas []string `json:"avoid_areas,omitempty"`
}

// ParseOrgSettings deserializes the JSONB settings column into OrgSettings,
// applying defaults for any missing or zero-valued fields.
func ParseOrgSettings(raw json.RawMessage) OrgSettings {
	var s OrgSettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}

	if s.AutonomyLevel == "" {
		s.AutonomyLevel = DefaultAutonomyLevel
	}
	if s.Aggressiveness == 0 {
		s.Aggressiveness = DefaultAggressiveness
	}
	if s.MaxConcurrentRuns == 0 {
		s.MaxConcurrentRuns = DefaultMaxConcurrentRuns
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
		s.PMScheduleHours = DefaultPMScheduleHours
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
	if s.ProductContext == nil && s.ProductDirection != "" {
		s.ProductContext = &ProductContext{
			Direction: s.ProductDirection,
		}
	}
	return s
}
