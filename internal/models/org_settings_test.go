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
		"pm_model": "claude-sonnet-4-5",
		"product_context": {
			"philosophy": "Prefer minimal diffs",
			"direction": "Harden billing",
			"focus_areas": ["billing", "api"],
			"avoid_areas": ["legacy-auth"]
		},
		"llm_model": "gpt-5.4-mini",
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
	require.Equal(t, "claude-sonnet-4-5", s.PMModel, "should override pm_model")
	require.NotNil(t, s.ProductContext, "should parse product_context")
	require.Equal(t, "Prefer minimal diffs", s.ProductContext.Philosophy, "should parse product_context.philosophy")
	require.Equal(t, "Harden billing", s.ProductContext.Direction, "should parse product_context.direction")
	require.Equal(t, []string{"billing", "api"}, s.ProductContext.FocusAreas, "should parse product_context.focus_areas")
	require.Equal(t, []string{"legacy-auth"}, s.ProductContext.AvoidAreas, "should parse product_context.avoid_areas")
	require.Equal(t, "gpt-5.4-mini", s.LLMModel, "should override llm_model")
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
			"claude_code": {"ANTHROPIC_MODEL": "claude-opus-4-7", "ANTHROPIC_API_KEY": "sk-ant-org"},
			"gemini_cli": {"GEMINI_MODEL": "gemini-2.5-pro"}
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.NotNil(t, s.AgentConfig, "should parse agent_config")
	require.Equal(t, "claude-opus-4-7", s.AgentConfig["claude_code"]["ANTHROPIC_MODEL"])
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
	require.NoError(t, AgentTypeAmp.Validate())
	require.NoError(t, AgentTypePi.Validate())
	// pm_agent is intentionally rejected: it's an internal agent type used by
	// the PM service for its own scheduled runs, never a user-selectable
	// default_agent_type on OrgSettings.
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
		{name: "xhigh is valid", effort: ReasoningEffortXHigh},
		{name: "max is valid", effort: ReasoningEffortMax},
		{name: "rejects invalid value", effort: "invalid", wantErr: true},
		{name: "rejects unknown value", effort: "turbo", wantErr: true},
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

func TestAgentType_SupportsReasoningEffort(t *testing.T) {
	t.Parallel()

	require.True(t, AgentTypeCodex.SupportsReasoningEffort(), "Codex should support explicit reasoning overrides")
	require.True(t, AgentTypeClaudeCode.SupportsReasoningEffort(), "Claude Code should support explicit reasoning overrides")
	require.False(t, AgentTypeGeminiCLI.SupportsReasoningEffort(), "Gemini CLI should not report reasoning override support")
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

func TestOrgSize_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, OrgSizeSmall.Validate())
	require.NoError(t, OrgSizeMedium.Validate())
	require.NoError(t, OrgSizeLarge.Validate())
	require.NoError(t, OrgSizeEnterprise.Validate())
	require.Error(t, OrgSize("").Validate(), "empty string should be invalid")
	require.Error(t, OrgSize("huge").Validate())
}

func TestOrgSize_ContextLimits(t *testing.T) {
	t.Parallel()

	small := OrgSizeSmall.ContextLimits()
	require.Equal(t, 50, small.MaxOpenIssues, "small orgs should have lower issue limit")
	require.Equal(t, 30_000, small.PMMaxTokens, "small orgs should have lower PM token limit")

	medium := OrgSizeMedium.ContextLimits()
	require.Equal(t, 100, medium.MaxOpenIssues, "medium should match previous defaults")
	require.Equal(t, 50_000, medium.PMMaxTokens, "medium should match previous PM token default")
	require.Equal(t, 50_000, medium.AgentLowTokenMax, "medium low token should match previous default")
	require.Equal(t, 200_000, medium.AgentHighTokenMax, "medium high token should match previous default")

	large := OrgSizeLarge.ContextLimits()
	require.Equal(t, 300, large.MaxOpenIssues, "large orgs should see more issues")
	require.Equal(t, 100_000, large.PMMaxTokens, "large orgs should have higher PM token limit")

	enterprise := OrgSizeEnterprise.ContextLimits()
	require.Equal(t, 500, enterprise.MaxOpenIssues, "enterprise orgs should see most issues")
	require.Equal(t, 150_000, enterprise.PMMaxTokens, "enterprise orgs should have highest PM token limit")
	require.Equal(t, 75_000, enterprise.AgentLowTokenMax, "enterprise low tokens should be elevated")
	require.Equal(t, 250_000, enterprise.AgentHighTokenMax, "enterprise high tokens should be elevated")

	// Verify description truncation decreases for larger orgs (more issues = less per-issue budget)
	require.Greater(t, small.IssueDescriptionMax, enterprise.IssueDescriptionMax,
		"larger orgs should have shorter per-issue descriptions to fit more issues in context")
}

func TestOrgSize_PMScheduleHours(t *testing.T) {
	t.Parallel()

	require.Equal(t, 6, OrgSizeSmall.PMScheduleHours(), "small orgs run PM less often")
	require.Equal(t, 24, OrgSizeMedium.PMScheduleHours(), "medium orgs should default to daily PM runs")
	require.Equal(t, 2, OrgSizeLarge.PMScheduleHours(), "large orgs need more frequent PM")
	require.Equal(t, 1, OrgSizeEnterprise.PMScheduleHours(), "enterprise orgs need hourly PM")
}

func TestOrgSize_MaxConcurrentRuns(t *testing.T) {
	t.Parallel()

	require.Equal(t, 5, OrgSizeSmall.MaxConcurrentRuns())
	require.Equal(t, 10, OrgSizeMedium.MaxConcurrentRuns())
	require.Equal(t, 15, OrgSizeLarge.MaxConcurrentRuns())
	require.Equal(t, 25, OrgSizeEnterprise.MaxConcurrentRuns())
}

func TestContextLimits_WithDefaults(t *testing.T) {
	t.Parallel()

	defaults := OrgSizeLarge.ContextLimits()

	t.Run("fills all zero fields", func(t *testing.T) {
		t.Parallel()
		empty := ContextLimits{}
		result := empty.WithDefaults(defaults)
		require.Equal(t, defaults, result, "all-zero input should produce the defaults")
	})

	t.Run("preserves explicit values", func(t *testing.T) {
		t.Parallel()
		partial := ContextLimits{
			MaxOpenIssues: 400,
			PMMaxTokens:   120_000,
		}
		result := partial.WithDefaults(defaults)
		require.Equal(t, 400, result.MaxOpenIssues, "explicit value should be preserved")
		require.Equal(t, 120_000, result.PMMaxTokens, "explicit value should be preserved")
		require.Equal(t, defaults.MaxTriagedIssues, result.MaxTriagedIssues, "zero field should get default")
		require.Equal(t, defaults.AgentHighTokenMax, result.AgentHighTokenMax, "zero field should get default")
	})

	t.Run("idempotent on complete input", func(t *testing.T) {
		t.Parallel()
		complete := OrgSizeEnterprise.ContextLimits()
		result := complete.WithDefaults(defaults)
		require.Equal(t, complete, result, "already-complete input should be unchanged")
	})
}

func TestParseOrgSettings_OrgSizeDefaults(t *testing.T) {
	t.Parallel()

	// Large org should get size-appropriate defaults
	raw := json.RawMessage(`{"org_size": "large"}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, OrgSizeLarge, s.OrgSize)
	require.Equal(t, 15, s.MaxConcurrentRuns, "large org should default to 15 concurrent runs")
	require.Equal(t, 2, s.PMScheduleHours, "large org should default to 2-hour PM schedule")
	require.Equal(t, 300, s.ContextLimits.MaxOpenIssues, "large org should see 300 open issues")
	require.Equal(t, 100_000, s.ContextLimits.PMMaxTokens, "large org should get 100k PM tokens")
}

func TestParseOrgSettings_OrgSizeWithOverrides(t *testing.T) {
	t.Parallel()

	// Explicit overrides should take precedence over size defaults
	raw := json.RawMessage(`{
		"org_size": "large",
		"max_concurrent_runs": 15,
		"pm_schedule_hours": 3,
		"context_limits": {
			"max_open_issues": 400,
			"pm_max_tokens": 120000
		}
	}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, 15, s.MaxConcurrentRuns, "explicit override should win over size default")
	require.Equal(t, 3, s.PMScheduleHours, "explicit override should win over size default")
	require.Equal(t, 400, s.ContextLimits.MaxOpenIssues, "explicit context limit should win")
	require.Equal(t, 120_000, s.ContextLimits.PMMaxTokens, "explicit token limit should win")
	// Non-overridden fields should still get size defaults
	require.Equal(t, 200, s.ContextLimits.MaxTriagedIssues, "non-overridden should use size default")
}

func TestParseOrgSettings_DefaultOrgSizeIsMedium(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err)

	// With no org_size set, defaults should match medium profile (backward compatible)
	require.Equal(t, 10, s.MaxConcurrentRuns, "default should match medium concurrent runs")
	require.Equal(t, 24, s.PMScheduleHours, "default should match medium PM schedule")
	require.Equal(t, 100, s.ContextLimits.MaxOpenIssues, "default should match medium open issues")
	require.Equal(t, 50_000, s.ContextLimits.PMMaxTokens, "default should match medium PM tokens")
	require.Equal(t, 50_000, s.ContextLimits.AgentLowTokenMax, "default should match medium low tokens")
	require.Equal(t, 200_000, s.ContextLimits.AgentHighTokenMax, "default should match medium high tokens")
}

func TestParseOrgSettings_PRAuthorship_Default(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{}`))
	require.NoError(t, err)
	require.Equal(t, PRAuthorshipUserPreferred, s.PRAuthorship, "should default to user_preferred")
	require.False(t, s.PRDraftDefault, "should default to non-draft PRs")
}

func TestParseOrgSettings_PRAuthorship_Explicit(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"pr_authorship":"app_only","pr_draft_default":true}`))
	require.NoError(t, err)
	require.Equal(t, PRAuthorshipAppOnly, s.PRAuthorship, "should parse app_only authorship")
	require.True(t, s.PRDraftDefault, "should parse draft default")
}

func TestPRAuthorship_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, PRAuthorshipUserPreferred.Validate())
	require.NoError(t, PRAuthorshipAppOnly.Validate())
	require.NoError(t, PRAuthorshipUserRequired.Validate())
	require.NoError(t, PRAuthorship("").Validate(), "empty should be valid")
	require.Error(t, PRAuthorship("invalid").Validate(), "unknown value should be invalid")
}

func TestParseOrgSettings_MaxSessionDuration_Default(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err)
	require.Equal(t, DefaultMaxSessionDurationSeconds, s.MaxSessionDurationSeconds, "unset should default")
}

func TestParseOrgSettings_MaxSessionDuration_Zero(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"max_session_duration_seconds":0}`))
	require.NoError(t, err)
	require.Equal(t, DefaultMaxSessionDurationSeconds, s.MaxSessionDurationSeconds, "zero should default")
}

func TestParseOrgSettings_MaxSessionDuration_ClampsBelowMin(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"max_session_duration_seconds":30}`))
	require.NoError(t, err)
	require.Equal(t, MinMaxSessionDurationSeconds, s.MaxSessionDurationSeconds, "below min should clamp up")
}

func TestParseOrgSettings_MaxSessionDuration_ClampsAboveMax(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"max_session_duration_seconds":99999}`))
	require.NoError(t, err)
	require.Equal(t, MaxMaxSessionDurationSeconds, s.MaxSessionDurationSeconds, "above max should clamp down")
}

func TestParseOrgSettings_MaxSessionDuration_InRange(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"max_session_duration_seconds":600}`))
	require.NoError(t, err)
	require.Equal(t, 600, s.MaxSessionDurationSeconds, "in-range value should pass through")
}

func TestParseOrgSettings_RuntimeBudgets_Defaults(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err, "ParseOrgSettings should apply runtime budget defaults")
	require.Equal(t, DefaultNoProgressTimeoutSeconds, s.RuntimeBudgets.NoProgressTimeoutSeconds, "no-progress timeout should default")
	require.Equal(t, DefaultGracefulShutdownWindowSeconds, s.RuntimeBudgets.GracefulShutdownWindowSeconds, "graceful shutdown window should default")
	require.Equal(t, DefaultCheckpointFinalizeWindowSeconds, s.RuntimeBudgets.CheckpointFinalizationWindowSeconds, "checkpoint finalization window should default")
	require.Equal(t, DefaultAutomaticExtensionSeconds, s.RuntimeBudgets.AutomaticExtensionSeconds, "automatic extension window should default")
	require.Equal(t, DefaultMaxAutomaticExtensionSeconds, s.RuntimeBudgets.MaxAutomaticExtensionSeconds, "max automatic extension should default")
	require.Equal(t, DefaultAbsoluteRuntimeCeilingSeconds, s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds, "absolute runtime ceiling should default")
}

func TestParseOrgSettings_RuntimeBudgets_ClampToSoftBudgetAndCeiling(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"max_session_duration_seconds": 1200,
		"runtime_budgets": {
			"max_automatic_extension_seconds": 7200,
			"absolute_runtime_ceiling_seconds": 1500
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err, "ParseOrgSettings should clamp runtime budgets against the soft budget and ceiling")
	require.Equal(t, 1200, s.MaxSessionDurationSeconds, "soft budget should preserve the configured value")
	require.Equal(t, 1500, s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds, "absolute runtime ceiling should preserve the configured value")
	require.Equal(t, 300, s.RuntimeBudgets.MaxAutomaticExtensionSeconds, "max automatic extension should clamp to the available headroom")
}

func TestParseOrgSettings_RuntimeBudgets_ClampsAbsoluteCeilingToWorkerWatchdog(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"max_session_duration_seconds": 7200,
		"runtime_budgets": {
			"absolute_runtime_ceiling_seconds": 21600
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err, "ParseOrgSettings should clamp oversized absolute runtime ceilings")
	require.Equal(t, MaxAbsoluteRuntimeCeilingSeconds, s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds, "absolute runtime ceiling should not exceed the worker-supported ceiling")
}

func TestParseOrgSettings_RuntimeBudgets_NegativeMaxAutomaticExtensionClampsToZero(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"max_session_duration_seconds": 900,
		"runtime_budgets": {
			"max_automatic_extension_seconds": -30,
			"absolute_runtime_ceiling_seconds": 1200
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err, "ParseOrgSettings should accept negative max automatic extension values and clamp them")
	require.Equal(t, 1200, s.RuntimeBudgets.AbsoluteRuntimeCeilingSeconds, "absolute runtime ceiling should preserve the configured value")
	require.Equal(t, 0, s.RuntimeBudgets.MaxAutomaticExtensionSeconds, "negative max automatic extension should clamp to zero rather than defaulting positive")
}

func TestLinearAutomationSettingsEffectiveAccessors(t *testing.T) {
	t.Parallel()

	f := false
	settings := LinearAutomationSettings{}
	require.True(t, settings.EffectivePostSessionLinks(), "missing post-session-links flag should default true")
	require.True(t, settings.EffectiveMoveWorkflowStates(), "missing move-workflow-states flag should default true")

	settings = LinearAutomationSettings{
		PostSessionLinks:   &f,
		MoveWorkflowStates: &f,
	}
	require.False(t, settings.EffectivePostSessionLinks(), "explicit false post-session-links should be honored")
	require.False(t, settings.EffectiveMoveWorkflowStates(), "explicit false move-workflow-states should be honored")
}

func TestLinearAutomationSettingsPerTeamOverrides(t *testing.T) {
	t.Parallel()

	f := false
	settings := LinearAutomationSettings{
		PerTeam: map[string]LinearTeamAutomationOverride{
			"ACS": {
				PostSessionLinks:   &f,
				MoveWorkflowStates: &f,
			},
		},
	}

	require.False(t, settings.PostSessionLinksFor("ACS"), "team override should disable post-session links")
	require.False(t, settings.MoveWorkflowStatesFor("ACS"), "team override should disable workflow moves")
	require.True(t, settings.PostSessionLinksFor("ENG"), "missing team override should inherit org default")
	require.True(t, settings.MoveWorkflowStatesFor("ENG"), "missing team override should inherit org default")
}
