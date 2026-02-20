package models

import "encoding/json"

// AgentEnvConfig holds per-agent environment variable overrides.
// Keys are agent type names (e.g. "claude_code", "gemini_cli", "codex"),
// values are maps of env var name → value.
type AgentEnvConfig map[string]map[string]string

// OrgSettings is the strongly-typed representation of organizations.settings JSONB.
type OrgSettings struct {
	AutonomyLevel        string               `json:"autonomy_level"`
	Aggressiveness       int                  `json:"execution_aggressiveness"`
	MaxConcurrentRuns    int                  `json:"max_concurrent_runs"`
	ConfidenceThresholds ConfidenceThresholds `json:"confidence_thresholds"`
	PriorityWeights      PriorityWeights      `json:"priority_weights"`
	MinPriorityThreshold float64              `json:"min_priority_threshold"`
	ProductDirection     string               `json:"product_direction"`
	LLMModel             string               `json:"llm_model"`
	AgentConfig          AgentEnvConfig       `json:"agent_config,omitempty"`
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
	DefaultAutonomyLevel        = "manual"
	DefaultAggressiveness       = 5
	DefaultMaxConcurrentRuns    = 3
	DefaultMinPriorityThreshold = 30.0

	DefaultWeightCustomerImpact = 0.35
	DefaultWeightSeverity       = 0.25
	DefaultWeightRecency        = 0.20
	DefaultWeightRevenueRisk    = 0.20

	DefaultConfidenceAutoProceed = 0.85
	DefaultConfidenceHumanReview = 0.60
)

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
	if s.MinPriorityThreshold == 0 {
		s.MinPriorityThreshold = DefaultMinPriorityThreshold
	}
	if s.PriorityWeights == (PriorityWeights{}) {
		s.PriorityWeights = PriorityWeights{
			CustomerImpact: DefaultWeightCustomerImpact,
			Severity:       DefaultWeightSeverity,
			Recency:        DefaultWeightRecency,
			RevenueRisk:    DefaultWeightRevenueRisk,
		}
	}
	if s.ConfidenceThresholds == (ConfidenceThresholds{}) {
		s.ConfidenceThresholds = ConfidenceThresholds{
			AutoProceed: DefaultConfidenceAutoProceed,
			HumanReview: DefaultConfidenceHumanReview,
		}
	}
	return s
}
