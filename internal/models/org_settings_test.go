package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOrgSettings_Defaults(t *testing.T) {
	t.Parallel()

	s := ParseOrgSettings(nil)

	require.Equal(t, DefaultAutonomyLevel, s.AutonomyLevel, "should default autonomy_level")
	require.Equal(t, DefaultAggressiveness, s.Aggressiveness, "should default aggressiveness")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs")
	require.Equal(t, DefaultMinPriorityThreshold, s.MinPriorityThreshold, "should default min_priority_threshold")
	require.Equal(t, DefaultWeightCustomerImpact, s.PriorityWeights.CustomerImpact, "should default customer_impact weight")
	require.Equal(t, DefaultWeightSeverity, s.PriorityWeights.Severity, "should default severity weight")
	require.Equal(t, DefaultWeightRecency, s.PriorityWeights.Recency, "should default recency weight")
	require.Equal(t, DefaultWeightRevenueRisk, s.PriorityWeights.RevenueRisk, "should default revenue_risk weight")
	require.Equal(t, DefaultConfidenceAutoProceed, s.ConfidenceThresholds.AutoProceed, "should default auto_proceed threshold")
	require.Equal(t, DefaultConfidenceHumanReview, s.ConfidenceThresholds.HumanReview, "should default human_review threshold")
	require.Empty(t, s.LLMModel, "should default llm_model to empty")
	require.Empty(t, s.ProductDirection, "should default product_direction to empty")
}

func TestParseOrgSettings_EmptyJSON(t *testing.T) {
	t.Parallel()

	s := ParseOrgSettings(json.RawMessage(`{}`))

	require.Equal(t, DefaultAutonomyLevel, s.AutonomyLevel, "should default autonomy_level for empty JSON")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs for empty JSON")
}

func TestParseOrgSettings_OverrideValues(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"autonomy_level": "auto_all",
		"execution_aggressiveness": 8,
		"max_concurrent_runs": 10,
		"min_priority_threshold": 50.0,
		"product_direction": "focus on billing",
		"llm_model": "gpt-4o",
		"confidence_thresholds": {
			"auto_proceed": 0.95,
			"human_review": 0.70
		},
		"priority_weights": {
			"customer_impact": 0.40,
			"severity": 0.30,
			"recency": 0.15,
			"revenue_risk": 0.15
		}
	}`)

	s := ParseOrgSettings(raw)

	require.Equal(t, "auto_all", s.AutonomyLevel, "should override autonomy_level")
	require.Equal(t, 8, s.Aggressiveness, "should override aggressiveness")
	require.Equal(t, 10, s.MaxConcurrentRuns, "should override max_concurrent_runs")
	require.Equal(t, 50.0, s.MinPriorityThreshold, "should override min_priority_threshold")
	require.Equal(t, "focus on billing", s.ProductDirection, "should override product_direction")
	require.Equal(t, "gpt-4o", s.LLMModel, "should override llm_model")
	require.Equal(t, 0.95, s.ConfidenceThresholds.AutoProceed, "should override auto_proceed")
	require.Equal(t, 0.70, s.ConfidenceThresholds.HumanReview, "should override human_review")
	require.Equal(t, 0.40, s.PriorityWeights.CustomerImpact, "should override customer_impact")
	require.Equal(t, 0.30, s.PriorityWeights.Severity, "should override severity")
	require.Equal(t, 0.15, s.PriorityWeights.Recency, "should override recency")
	require.Equal(t, 0.15, s.PriorityWeights.RevenueRisk, "should override revenue_risk")
}

func TestParseOrgSettings_PartialOverride(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"autonomy_level": "auto_simple", "llm_model": "claude-sonnet-4-5"}`)
	s := ParseOrgSettings(raw)

	require.Equal(t, "auto_simple", s.AutonomyLevel, "should override autonomy_level")
	require.Equal(t, "claude-sonnet-4-5", s.LLMModel, "should override llm_model")
	require.Equal(t, DefaultAggressiveness, s.Aggressiveness, "should default aggressiveness when not provided")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs when not provided")
}

func TestParseOrgSettings_AgentConfig(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"agent_config": {
			"claude_code": {"ANTHROPIC_MODEL": "opus", "ANTHROPIC_API_KEY": "sk-ant-org"},
			"gemini_cli": {"GEMINI_MODEL": "gemini-2.5-pro"}
		}
	}`)

	s := ParseOrgSettings(raw)

	require.NotNil(t, s.AgentConfig, "should parse agent_config")
	require.Equal(t, "opus", s.AgentConfig["claude_code"]["ANTHROPIC_MODEL"])
	require.Equal(t, "sk-ant-org", s.AgentConfig["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "gemini-2.5-pro", s.AgentConfig["gemini_cli"]["GEMINI_MODEL"])
	require.NotContains(t, s.AgentConfig, "codex", "codex should not be present when not configured")
}

func TestParseOrgSettings_AgentConfigEmpty(t *testing.T) {
	t.Parallel()

	s := ParseOrgSettings(json.RawMessage(`{}`))

	require.Nil(t, s.AgentConfig, "agent_config should be nil for empty JSON")
}

func TestParseOrgSettings_InvalidJSON(t *testing.T) {
	t.Parallel()

	s := ParseOrgSettings(json.RawMessage(`{invalid`))

	require.Equal(t, DefaultAutonomyLevel, s.AutonomyLevel, "should fall back to defaults on invalid JSON")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should fall back to defaults on invalid JSON")
}
