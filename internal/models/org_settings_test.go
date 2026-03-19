package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOrgSettings_Defaults(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err)

	require.Equal(t, DefaultAutonomyLevel, s.AutonomyLevel, "should default autonomy_level")
	require.Equal(t, DefaultAggressiveness, s.Aggressiveness, "should default aggressiveness")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs")
	require.Equal(t, DefaultMinPriorityThreshold, s.MinPriorityThreshold, "should default min_priority_threshold")
	require.Equal(t, DefaultWeightCustomerImpact, s.PriorityWeights.CustomerImpact, "should default customer_impact weight")
	require.Equal(t, DefaultWeightSeverity, s.PriorityWeights.Severity, "should default severity weight")
	require.Equal(t, DefaultWeightRecency, s.PriorityWeights.Recency, "should default recency weight")
	require.Equal(t, DefaultWeightRevenueRisk, s.PriorityWeights.RevenueRisk, "should default revenue_risk weight")
	require.Equal(t, DefaultAgentAutonomy, s.AgentAutonomy, "should default agent_autonomy")
	require.Equal(t, 0.4, s.ConfidenceThresholds.AutoProceed, "should derive auto_proceed from aggressive autonomy")
	require.Equal(t, 0.2, s.ConfidenceThresholds.HumanReview, "should derive human_review from aggressive autonomy")
	require.Empty(t, s.LLMModel, "should default llm_model to empty")
	require.Empty(t, s.ProductDirection, "should default product_direction to empty")
	require.Equal(t, DefaultPMScheduleHours, s.PMScheduleHours, "should default pm_schedule_hours")
	require.Equal(t, DefaultPMModel, s.PMModel, "should default pm_model")
	require.Nil(t, s.ProductContext, "should default product_context to nil")
}

func TestParseOrgSettings_EmptyJSON(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{}`))
	require.NoError(t, err)

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
		"agent_autonomy": "conservative",
		"product_direction": "focus on billing",
		"pm_schedule_hours": 6,
		"pm_model": "sonnet",
		"product_context": {
			"philosophy": "Prefer minimal diffs",
			"direction": "Harden billing",
			"focus_areas": ["billing", "api"],
			"avoid_areas": ["legacy-auth"]
		},
		"llm_model": "gpt-4o",
		"priority_weights": {
			"customer_impact": 0.40,
			"severity": 0.30,
			"recency": 0.15,
			"revenue_risk": 0.15
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, AutonomyLevelAutoAll, s.AutonomyLevel, "should override autonomy_level")
	require.Equal(t, 8, s.Aggressiveness, "should override aggressiveness")
	require.Equal(t, 10, s.MaxConcurrentRuns, "should override max_concurrent_runs")
	require.Equal(t, 50.0, s.MinPriorityThreshold, "should override min_priority_threshold")
	require.Equal(t, "focus on billing", s.ProductDirection, "should override product_direction")
	require.Equal(t, 6, s.PMScheduleHours, "should override pm_schedule_hours")
	require.Equal(t, "sonnet", s.PMModel, "should override pm_model")
	require.NotNil(t, s.ProductContext, "should parse product_context")
	require.Equal(t, "Prefer minimal diffs", s.ProductContext.Philosophy, "should parse product_context.philosophy")
	require.Equal(t, "Harden billing", s.ProductContext.Direction, "should parse product_context.direction")
	require.Equal(t, []string{"billing", "api"}, s.ProductContext.FocusAreas, "should parse product_context.focus_areas")
	require.Equal(t, []string{"legacy-auth"}, s.ProductContext.AvoidAreas, "should parse product_context.avoid_areas")
	require.Equal(t, "gpt-4o", s.LLMModel, "should override llm_model")
	require.Equal(t, "conservative", s.AgentAutonomy, "should override agent_autonomy")
	require.Equal(t, 1.0, s.ConfidenceThresholds.AutoProceed, "should derive auto_proceed from conservative autonomy")
	require.Equal(t, 0.8, s.ConfidenceThresholds.HumanReview, "should derive human_review from conservative autonomy")
	require.Equal(t, 0.40, s.PriorityWeights.CustomerImpact, "should override customer_impact")
	require.Equal(t, 0.30, s.PriorityWeights.Severity, "should override severity")
	require.Equal(t, 0.15, s.PriorityWeights.Recency, "should override recency")
	require.Equal(t, 0.15, s.PriorityWeights.RevenueRisk, "should override revenue_risk")
}

func TestParseOrgSettings_PartialOverride(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"autonomy_level": "auto_simple", "llm_model": "claude-sonnet-4-5"}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, AutonomyLevelAutoSimple, s.AutonomyLevel, "should override autonomy_level")
	require.Equal(t, "claude-sonnet-4-5", s.LLMModel, "should override llm_model")
	require.Equal(t, DefaultAggressiveness, s.Aggressiveness, "should default aggressiveness when not provided")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs when not provided")
}

func TestParseOrgSettings_ProductContextMigration(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"product_direction":"shift to reliability"}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, "shift to reliability", s.ProductDirection, "should preserve product_direction")
	require.NotNil(t, s.ProductContext, "should migrate product_direction into product_context")
	require.Equal(t, "shift to reliability", s.ProductContext.Direction, "should set product_context.direction from product_direction")
	require.Empty(t, s.ProductContext.Philosophy, "should default product_context.philosophy to empty")
}

func TestParseOrgSettings_AgentConfig(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"agent_config": {
			"claude_code": {"ANTHROPIC_MODEL": "opus", "ANTHROPIC_API_KEY": "sk-ant-org"},
			"gemini_cli": {"GEMINI_MODEL": "gemini-2.5-pro"}
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.NotNil(t, s.AgentConfig, "should parse agent_config")
	require.Equal(t, "opus", s.AgentConfig["claude_code"]["ANTHROPIC_MODEL"])
	require.Equal(t, "sk-ant-org", s.AgentConfig["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "gemini-2.5-pro", s.AgentConfig["gemini_cli"]["GEMINI_MODEL"])
	require.NotContains(t, s.AgentConfig, "codex", "codex should not be present when not configured")
}

func TestParseOrgSettings_AgentConfigEmpty(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{}`))
	require.NoError(t, err)

	require.Nil(t, s.AgentConfig, "agent_config should be nil for empty JSON")
}

func TestParseOrgSettings_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := ParseOrgSettings(json.RawMessage(`{invalid`))
	require.Error(t, err, "should return error on invalid JSON")
	require.Contains(t, err.Error(), "unmarshal org settings", "should wrap error")
}

func TestAutonomyLevel_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, AutonomyLevelManual.Validate())
	require.NoError(t, AutonomyLevelAutoSimple.Validate())
	require.NoError(t, AutonomyLevelAutoAll.Validate())
	require.Error(t, AutonomyLevel("invalid").Validate())
	require.Error(t, AutonomyLevel("").Validate())
}

func TestAgentType_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, AgentTypeClaudeCode.Validate())
	require.NoError(t, AgentTypeGeminiCLI.Validate())
	require.NoError(t, AgentTypeCodex.Validate())
	require.Error(t, AgentType("pm_agent").Validate())
	require.Error(t, AgentType("").Validate())
}

func TestReasoningEffort_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		effort  ReasoningEffort
		wantErr bool
	}{
		{name: "empty is valid", effort: ""},
		{name: "low is valid", effort: ReasoningEffortLow},
		{name: "medium is valid", effort: ReasoningEffortMedium},
		{name: "high is valid", effort: ReasoningEffortHigh},
		{name: "rejects invalid value", effort: "invalid", wantErr: true},
		{name: "rejects unknown value", effort: "max", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.effort.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid reasoning effort")
			} else {
				require.NoError(t, err, "Validate should accept valid reasoning effort")
			}
		})
	}
}

func TestConfidenceThresholdsForAutonomy(t *testing.T) {
	t.Parallel()

	conservative := ConfidenceThresholdsForAutonomy(AgentAutonomyConservative)
	require.Equal(t, 1.0, conservative.AutoProceed)
	require.Equal(t, 0.8, conservative.HumanReview)

	balanced := ConfidenceThresholdsForAutonomy(AgentAutonomyBalanced)
	require.Equal(t, 0.85, balanced.AutoProceed)
	require.Equal(t, 0.5, balanced.HumanReview)

	aggressive := ConfidenceThresholdsForAutonomy(AgentAutonomyAggressive)
	require.Equal(t, 0.4, aggressive.AutoProceed)
	require.Equal(t, 0.2, aggressive.HumanReview)

	// unknown defaults to balanced
	unknown := ConfidenceThresholdsForAutonomy("unknown")
	require.Equal(t, balanced, unknown)
}
