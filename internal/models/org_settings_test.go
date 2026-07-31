package models

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseOrgSettings_Defaults(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err)

	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs")
	require.Equal(t, DefaultWeightCustomerImpact, s.PriorityWeights.CustomerImpact, "should default customer_impact weight")
	require.Equal(t, DefaultWeightSeverity, s.PriorityWeights.Severity, "should default severity weight")
	require.Equal(t, DefaultWeightRecency, s.PriorityWeights.Recency, "should default recency weight")
	require.Equal(t, DefaultWeightRevenueRisk, s.PriorityWeights.RevenueRisk, "should default revenue_risk weight")
	require.Empty(t, s.LLMModel, "should default llm_model to empty")
	require.Equal(t, CodexModelGPT56Sol, s.CodingAgentModelDefaults[AgentTypeCodex], "Codex should default to GPT 5.6 Sol")
	require.Equal(t, ClaudeCodeModelOpus5, s.CodingAgentModelDefaults[AgentTypeClaudeCode], "Claude Code should default to Opus 5")
	require.Equal(t, ReasoningEffortHigh, s.CodingAgentReasoningDefaults[AgentTypeCodex], "Codex should default to high reasoning")
	require.Equal(t, ReasoningEffortMax, s.CodingAgentReasoningDefaults[AgentTypeClaudeCode], "Claude Code should default to max reasoning")
	require.Equal(t, DefaultCodingAgentModelDefaults, s.CodingAgentModelDefaults, "back-filled model defaults should match the shared constants")
	require.Equal(t, DefaultCodingAgentReasoningDefaults, s.CodingAgentReasoningDefaults, "back-filled reasoning defaults should match the shared constants")
	require.Nil(t, s.ProductContext, "should default product_context to nil")
	require.True(t, s.EffectiveCodingAgentTabToolsEnabled(), "agent tab tools should default on")
	require.True(t, s.EffectiveAutoArchiveOnPRClose(), "auto-archive on PR close should default on")
	require.Equal(t, DefaultPreviewMaxPreviewsPerUser, s.PreviewMaxPreviewsPerUser, "should default per-user preview capacity")
	require.False(t, s.SandboxNetwork.StaticEgressEnabled, "static egress should be disabled by default")
}

func TestOrgSettings_EffectiveAutoArchiveOnPRClose(t *testing.T) {
	t.Parallel()

	f := false
	tVal := true
	tests := []struct {
		name     string
		settings OrgSettings
		expected bool
	}{
		{name: "missing defaults on", settings: OrgSettings{}, expected: true},
		{name: "explicit false disables", settings: OrgSettings{AutoArchiveOnPRClose: &f}, expected: false},
		{name: "explicit true enables", settings: OrgSettings{AutoArchiveOnPRClose: &tVal}, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.settings.EffectiveAutoArchiveOnPRClose(), "effective auto-archive setting should match expected value")
		})
	}
}

func TestOrgSettings_EffectiveCodingAgentTabToolsEnabled(t *testing.T) {
	t.Parallel()

	f := false
	tVal := true
	tests := []struct {
		name     string
		settings OrgSettings
		expected bool
	}{
		{name: "missing defaults on", settings: OrgSettings{}, expected: true},
		{name: "explicit false disables", settings: OrgSettings{CodingAgentTabToolsEnabled: &f}, expected: false},
		{name: "explicit true enables", settings: OrgSettings{CodingAgentTabToolsEnabled: &tVal}, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.settings.EffectiveCodingAgentTabToolsEnabled(), "effective tab-tool setting should match expected value")
		})
	}
}

func TestParseOrgSettings_EmptyJSON(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{}`))
	require.NoError(t, err)

	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs for empty JSON")
}

func TestParseOrgSettings_DefaultWorkRepositoryID(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	raw := json.RawMessage(`{"default_work_repository_id":"` + repoID.String() + `"}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err, "ParseOrgSettings should accept the shared default work repository")
	require.NotNil(t, s.DefaultWorkRepositoryID, "shared default work repository should parse when configured")
	require.Equal(t, repoID, *s.DefaultWorkRepositoryID, "shared default work repository should round-trip from JSON")
}

func TestParseOrgSettings_SessionAutomation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    AutomaticFollowThroughOrgSettings
		wantErr string
	}{
		{
			name: "defaults automatic follow through off",
			raw:  json.RawMessage(`{}`),
			want: AutomaticFollowThroughOrgSettings{},
		},
		{
			name: "parses automatic follow through settings",
			raw:  json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop":true,"readiness_after_review_loop_states":["clean"],"resolve_conflicts_when_idle":true,"fix_tests_when_idle":true}}}`),
			want: AutomaticFollowThroughOrgSettings{
				ReadinessAfterReviewLoop:       true,
				ReadinessAfterReviewLoopStates: []ReviewLoopStatus{ReviewLoopStatusClean},
				ResolveConflictsWhenIdle:       true,
				FixTestsWhenIdle:               true,
			},
		},
		{
			name:    "rejects non-terminal readiness states",
			raw:     json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop_states":["running"]}}}`),
			wantErr: "not terminal",
		},
		{
			name:    "rejects invalid readiness states",
			raw:     json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop_states":["unknown"]}}}`),
			wantErr: "invalid ReviewLoopStatus",
		},
		{
			name:    "rejects empty allowlist when bot mode is allowlist",
			raw:     json.RawMessage(`{"session_automation":{"automatic_follow_through":{"pr_feedback_bot_mode":"allowlist"}}}`),
			wantErr: "pr_feedback_bot_allowlist must not be empty",
		},
		{
			name:    "rejects blank allowlist entries",
			raw:     json.RawMessage(`{"session_automation":{"automatic_follow_through":{"pr_feedback_bot_mode":"allowlist","pr_feedback_bot_allowlist":["  "]}}}`),
			wantErr: "pr_feedback_bot_allowlist entries must not be blank",
		},
		{
			name: "parses valid bot allowlist",
			raw:  json.RawMessage(`{"session_automation":{"automatic_follow_through":{"pr_feedback_bot_mode":"allowlist","pr_feedback_bot_allowlist":["dependabot[bot]"]}}}`),
			want: AutomaticFollowThroughOrgSettings{
				PRFeedbackBotMode:      PRFeedbackBotModeAllowlist,
				PRFeedbackBotAllowlist: []string{"dependabot[bot]"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseOrgSettings(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err, "ParseOrgSettings should reject invalid session automation settings")
				require.Contains(t, err.Error(), tt.wantErr, "ParseOrgSettings should explain invalid session automation settings")
				return
			}
			require.NoError(t, err, "ParseOrgSettings should accept session automation settings")
			require.Equal(t, tt.want, got.SessionAutomation.AutomaticFollowThrough, "ParseOrgSettings should decode session automation settings")
		})
	}
}

func TestDefaultNewOrganizationSettings_EnablesAutomaticRepair(t *testing.T) {
	t.Parallel()

	settings, err := ParseOrgSettings(DefaultNewOrganizationSettings())
	require.NoError(t, err, "DefaultNewOrganizationSettings should produce valid org settings")
	require.True(t, settings.SessionAutomation.AutomaticFollowThrough.ResolveConflictsWhenIdle, "new organizations should default automatic conflict repair on")
	require.True(t, settings.SessionAutomation.AutomaticFollowThrough.FixTestsWhenIdle, "new organizations should default automatic test repair on")
	require.False(t, settings.SessionAutomation.AutomaticFollowThrough.ReadinessAfterReviewLoop, "new organizations should not change the readiness default")
}

func TestDefaultNewOrganizationSettings_LeavesCodingAgentDefaultsUnpinned(t *testing.T) {
	t.Parallel()

	// Persisting the defaults would pin a new org to today's values while every
	// pre-existing org keeps tracking the constant, so the two cohorts would drift
	// apart the first time a platform default is bumped.
	var seeded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(DefaultNewOrganizationSettings(), &seeded), "seeded settings should be valid JSON")
	require.NotContains(t, seeded, "coding_agent_model_defaults", "new orgs should inherit model defaults, not persist them")
	require.NotContains(t, seeded, "coding_agent_reasoning_defaults", "new orgs should inherit reasoning defaults, not persist them")

	settings, err := ParseOrgSettings(DefaultNewOrganizationSettings())
	require.NoError(t, err, "DefaultNewOrganizationSettings should produce valid org settings")
	require.NoError(t, ValidateSettingsModels(settings), "back-filled coding-agent defaults should pass settings validation")
	require.Equal(t, DefaultCodingAgentModelDefaults, settings.CodingAgentModelDefaults, "a new org should still resolve the shared model defaults")
	require.Equal(t, DefaultCodingAgentReasoningDefaults, settings.CodingAgentReasoningDefaults, "a new org should still resolve the shared reasoning defaults")
}

func TestParseOrgSettings_PreservesExplicitCodingAgentOptOut(t *testing.T) {
	t.Parallel()

	// An admin choosing "provider default" / "agent default" stores an empty
	// string. Key presence — not value — has to gate the back-fill, or the opt-out
	// gets silently overwritten on the next parse.
	raw := json.RawMessage(`{"coding_agent_model_defaults":{"codex":""},"coding_agent_reasoning_defaults":{"codex":""}}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Empty(t, s.CodingAgentModelDefaults[AgentTypeCodex], "explicit empty Codex model should survive parsing")
	require.Empty(t, s.CodingAgentReasoningDefaults[AgentTypeCodex], "explicit empty Codex reasoning should survive parsing")
	require.Equal(t, ClaudeCodeModelOpus5, s.CodingAgentModelDefaults[AgentTypeClaudeCode], "untouched agents should still be back-filled")
	require.Equal(t, ReasoningEffortMax, s.CodingAgentReasoningDefaults[AgentTypeClaudeCode], "untouched agents should still be back-filled")
}

func TestResolveCodingAgentDefaults(t *testing.T) {
	t.Parallel()

	org := OrgSettings{
		CodingAgentModelDefaults:     map[AgentType]string{AgentTypeCodex: CodexModelGPT55, AgentTypeClaudeCode: ClaudeCodeModelOpus5},
		CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{AgentTypeCodex: ReasoningEffortHigh, AgentTypeClaudeCode: ReasoningEffortMax},
	}
	personal := &UserSettings{
		CodingAgentModelDefault:      CodexModelGPT54,
		CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{AgentTypeCodex: ReasoningEffortLow},
	}

	tests := []struct {
		name          string
		agentType     AgentType
		model         string
		effort        ReasoningEffort
		personal      *UserSettings
		org           OrgSettings
		wantModel     string
		wantEffort    ReasoningEffort
		wantAssertion string
	}{
		{
			name: "request wins over every default", agentType: AgentTypeCodex,
			model: CodexModelGPT53Codex, effort: ReasoningEffortXHigh, personal: personal, org: org,
			wantModel: CodexModelGPT53Codex, wantEffort: ReasoningEffortXHigh,
			wantAssertion: "an explicit session selection should never be overridden",
		},
		{
			name: "personal defaults win over org defaults", agentType: AgentTypeCodex,
			personal: personal, org: org,
			wantModel: CodexModelGPT54, wantEffort: ReasoningEffortLow,
			wantAssertion: "a member's saved defaults should outrank the org defaults",
		},
		{
			name: "org defaults apply without personal settings", agentType: AgentTypeCodex,
			personal: nil, org: org,
			wantModel: CodexModelGPT55, wantEffort: ReasoningEffortHigh,
			wantAssertion: "org defaults should fill in for callers with no user context",
		},
		{
			name: "personal model for another agent is ignored", agentType: AgentTypeClaudeCode,
			personal: personal, org: org,
			wantModel: ClaudeCodeModelOpus5, wantEffort: ReasoningEffortMax,
			wantAssertion: "a Codex personal model should not leak into a Claude Code session",
		},
		{
			name: "unsupported org reasoning level is skipped", agentType: AgentTypeCodex,
			org:       OrgSettings{CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{AgentTypeCodex: ReasoningEffortMax}},
			wantModel: "", wantEffort: "",
			wantAssertion: "a level Codex cannot honor should degrade to no default rather than a 400",
		},
		{
			name: "retired org model default is skipped", agentType: AgentTypeCodex,
			org:       OrgSettings{CodingAgentModelDefaults: map[AgentType]string{AgentTypeCodex: "gpt-4-retired"}},
			wantModel: "", wantEffort: "",
			wantAssertion: "a default naming a model Codex no longer offers must not become an INVALID_MODEL for the whole org",
		},
		{
			name: "retired personal model default is skipped", agentType: AgentTypeCodex,
			personal:  &UserSettings{CodingAgentModelDefault: "gpt-4-retired"},
			org:       OrgSettings{CodingAgentModelDefaults: map[AgentType]string{AgentTypeCodex: CodexModelGPT55}},
			wantModel: CodexModelGPT55, wantEffort: "",
			wantAssertion: "a member's retired default should fall through to the org default, not fail the request",
		},
		{
			name: "an explicitly requested model is returned unchecked", agentType: AgentTypeCodex,
			model: "gpt-4-retired", org: org,
			wantModel: "gpt-4-retired", wantEffort: ReasoningEffortHigh,
			wantAssertion: "the caller's own bad value should still reach the handler's 400",
		},
		{
			name: "agents without defaults resolve to nothing", agentType: AgentTypeAmp,
			personal: personal, org: org,
			wantModel: "", wantEffort: "",
			wantAssertion: "agents with no configured default should be left untouched",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			model, effort := ResolveCodingAgentDefaults(tt.agentType, tt.model, tt.effort, tt.personal, tt.org)
			require.Equal(t, tt.wantModel, model, tt.wantAssertion)
			require.Equal(t, tt.wantEffort, effort, tt.wantAssertion)
		})
	}
}

func TestAutomaticFollowThroughOrgSettings_EffectiveReadinessAfterReviewLoopStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings AutomaticFollowThroughOrgSettings
		want     []ReviewLoopStatus
	}{
		{
			name:     "defaults to clean",
			settings: AutomaticFollowThroughOrgSettings{},
			want:     []ReviewLoopStatus{ReviewLoopStatusClean},
		},
		{
			name: "returns configured states",
			settings: AutomaticFollowThroughOrgSettings{
				ReadinessAfterReviewLoopStates: []ReviewLoopStatus{ReviewLoopStatusClean, ReviewLoopStatusFailed},
			},
			want: []ReviewLoopStatus{ReviewLoopStatusClean, ReviewLoopStatusFailed},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.settings.EffectiveReadinessAfterReviewLoopStates()
			require.Equal(t, tt.want, got, "EffectiveReadinessAfterReviewLoopStates should return expected states")
		})
	}
}

func TestParseOrgSettings_OverrideValues(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"max_concurrent_runs": 10,
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
		},
		"sandbox_network": {
			"static_egress_enabled": true
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, 10, s.MaxConcurrentRuns, "should override max_concurrent_runs")
	require.NotNil(t, s.ProductContext, "should parse product_context")
	require.Equal(t, "Prefer minimal diffs", s.ProductContext.Philosophy, "should parse product_context.philosophy")
	require.Equal(t, "Harden billing", s.ProductContext.Direction, "should parse product_context.direction")
	require.Equal(t, []string{"billing", "api"}, s.ProductContext.FocusAreas, "should parse product_context.focus_areas")
	require.Equal(t, []string{"legacy-auth"}, s.ProductContext.AvoidAreas, "should parse product_context.avoid_areas")
	require.Equal(t, "gpt-5.4-mini", s.LLMModel, "should override llm_model")
	require.Equal(t, 0.40, s.PriorityWeights.CustomerImpact, "should override customer_impact")
	require.Equal(t, 0.30, s.PriorityWeights.Severity, "should override severity")
	require.Equal(t, 0.15, s.PriorityWeights.Recency, "should override recency")
	require.Equal(t, 0.15, s.PriorityWeights.RevenueRisk, "should override revenue_risk")
	require.True(t, s.SandboxNetwork.StaticEgressEnabled, "should parse sandbox_network.static_egress_enabled")
}

func TestParseOrgSettings_PartialOverride(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"llm_model": "claude-sonnet-4-5"}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, "claude-sonnet-4-5", s.LLMModel, "should override llm_model")
	require.Equal(t, DefaultMaxConcurrentRuns, s.MaxConcurrentRuns, "should default max_concurrent_runs when not provided")
}

func TestParseOrgSettings_LegacyProductDirectionIsInert(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"product_direction":"shift to reliability"}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Nil(t, s.ProductContext, "legacy product_direction should remain inert")
}

func TestParseOrgSettings_AgentConfig(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"agent_config": {
			"claude_code": {"ANTHROPIC_MODEL": "claude-opus-4-7", "ANTHROPIC_API_KEY": "sk-ant-org"},
			"opencode": {"OPENCODE_MODEL": "google/gemini-3-flash"}
		}
	}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.NotNil(t, s.AgentConfig, "should parse agent_config")
	require.Equal(t, "claude-opus-4-7", s.AgentConfig["claude_code"]["ANTHROPIC_MODEL"])
	require.Equal(t, "sk-ant-org", s.AgentConfig["claude_code"]["ANTHROPIC_API_KEY"])
	require.Equal(t, "google/gemini-3-flash", s.AgentConfig["opencode"]["OPENCODE_MODEL"])
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

func TestAgentType_Validate(t *testing.T) {
	t.Parallel()

	require.NoError(t, AgentTypeClaudeCode.Validate())
	require.NoError(t, AgentTypeCodex.Validate())
	require.NoError(t, AgentTypeAmp.Validate())
	require.NoError(t, AgentTypePi.Validate())
	require.NoError(t, AgentTypeOpenCode.Validate())
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
	require.False(t, AgentTypeOpenCode.SupportsReasoningEffort(), "OpenCode should not report reasoning override support")
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

	medium := OrgSizeMedium.ContextLimits()
	require.Equal(t, 50_000, medium.AgentLowTokenMax, "medium low token should match previous default")
	require.Equal(t, 200_000, medium.AgentHighTokenMax, "medium high token should match previous default")

	enterprise := OrgSizeEnterprise.ContextLimits()
	require.Equal(t, 75_000, enterprise.AgentLowTokenMax, "enterprise low tokens should be elevated")
	require.Equal(t, 250_000, enterprise.AgentHighTokenMax, "enterprise high tokens should be elevated")
}

func TestOrgSize_MaxConcurrentRuns(t *testing.T) {
	t.Parallel()

	require.Equal(t, 2, OrgSizeSmall.MaxConcurrentRuns())
	require.Equal(t, 3, OrgSizeMedium.MaxConcurrentRuns())
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
			AgentLowTokenMax: 120_000,
		}
		result := partial.WithDefaults(defaults)
		require.Equal(t, 120_000, result.AgentLowTokenMax, "explicit value should be preserved")
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
	require.Equal(t, 50_000, s.ContextLimits.AgentLowTokenMax, "large org should retain the standard low token limit")
}

func TestParseOrgSettings_OrgSizeWithOverrides(t *testing.T) {
	t.Parallel()

	// Explicit overrides should take precedence over size defaults
	raw := json.RawMessage(`{
		"org_size": "large",
		"max_concurrent_runs": 15,
		"context_limits": {
			"agent_low_token_max": 120000
		}
	}`)
	s, err := ParseOrgSettings(raw)
	require.NoError(t, err)

	require.Equal(t, 15, s.MaxConcurrentRuns, "explicit override should win over size default")
	require.Equal(t, 120_000, s.ContextLimits.AgentLowTokenMax, "explicit token limit should win")
	require.Equal(t, 200_000, s.ContextLimits.AgentHighTokenMax, "non-overridden token limit should use the size default")
}

func TestParseOrgSettings_DefaultOrgSizeIsMedium(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err)

	// With no org_size set, defaults should match medium profile (backward compatible)
	require.Equal(t, 3, s.MaxConcurrentRuns, "default should match medium concurrent runs")
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
	require.Equal(t, 20*60, s.MaxSessionDurationSeconds, "unset max session duration should default to twenty minutes")
}

func TestParseOrgSettings_MaxSessionDuration_Zero(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(json.RawMessage(`{"max_session_duration_seconds":0}`))
	require.NoError(t, err)
	require.Equal(t, DefaultMaxSessionDurationSeconds, s.MaxSessionDurationSeconds, "zero should default")
	require.Equal(t, 20*60, s.MaxSessionDurationSeconds, "zero max session duration should default to twenty minutes")
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

func TestParseOrgSettings_PreviewMaxPreviewsPerUser(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected int
	}{
		{
			name:     "zero defaults",
			raw:      json.RawMessage(`{"preview_max_previews_per_user":0}`),
			expected: DefaultPreviewMaxPreviewsPerUser,
		},
		{
			name:     "custom value passes through",
			raw:      json.RawMessage(`{"preview_max_previews_per_user":7}`),
			expected: 7,
		},
		{
			name:     "below minimum clamps up",
			raw:      json.RawMessage(`{"preview_max_previews_per_user":-1}`),
			expected: MinPreviewMaxPreviewsPerUser,
		},
		{
			name:     "above maximum clamps down",
			raw:      json.RawMessage(`{"preview_max_previews_per_user":999}`),
			expected: MaxPreviewMaxPreviewsPerUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := ParseOrgSettings(tt.raw)
			require.NoError(t, err, "ParseOrgSettings should accept preview capacity settings")
			require.Equal(t, tt.expected, s.PreviewMaxPreviewsPerUser, "preview capacity should be normalized")
		})
	}
}

func TestParseOrgSettings_PreviewAutoPoolMaxActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected int
	}{
		{
			name:     "zero defaults",
			raw:      json.RawMessage(`{"preview_auto_pool_max_active":0}`),
			expected: DefaultPreviewAutoPoolMaxActive,
		},
		{
			name:     "custom value passes through",
			raw:      json.RawMessage(`{"preview_auto_pool_max_active":7}`),
			expected: 7,
		},
		{
			name:     "below minimum clamps up",
			raw:      json.RawMessage(`{"preview_auto_pool_max_active":-1}`),
			expected: MinPreviewAutoPoolMaxActive,
		},
		{
			name:     "above maximum clamps down",
			raw:      json.RawMessage(`{"preview_auto_pool_max_active":999}`),
			expected: MaxPreviewAutoPoolMaxActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := ParseOrgSettings(tt.raw)
			require.NoError(t, err, "ParseOrgSettings should accept auto-preview pool settings")
			require.Equal(t, tt.expected, s.PreviewAutoPoolMaxActive, "auto-preview pool capacity should be normalized")
		})
	}
}

func TestParseOrgSettings_PreviewSessionPrewarmMaxActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected int
	}{
		{
			name:     "zero stays disabled",
			raw:      json.RawMessage(`{"preview_session_prewarm_max_active":0}`),
			expected: DefaultPreviewSessionPrewarmMaxActive,
		},
		{
			name:     "custom value passes through",
			raw:      json.RawMessage(`{"preview_session_prewarm_max_active":10}`),
			expected: 10,
		},
		{
			name:     "below minimum clamps up",
			raw:      json.RawMessage(`{"preview_session_prewarm_max_active":-1}`),
			expected: MinPreviewSessionPrewarmMaxActive,
		},
		{
			name:     "above maximum clamps down",
			raw:      json.RawMessage(`{"preview_session_prewarm_max_active":999}`),
			expected: MaxPreviewSessionPrewarmMaxActive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := ParseOrgSettings(tt.raw)
			require.NoError(t, err, "ParseOrgSettings should accept session prewarm capacity settings")
			require.Equal(t, tt.expected, s.PreviewSessionPrewarmMaxActive, "session prewarm capacity should be normalized")
		})
	}
}

func TestParseOrgSettings_SandboxLifecycleDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		raw                  json.RawMessage
		expectedRetention    int
		expectedIdlePreview  int
		expectedPreviewHolds bool
	}{
		{
			name:                 "defaults",
			raw:                  json.RawMessage(`{}`),
			expectedRetention:    DefaultCompletedSessionRetentionMinutes,
			expectedIdlePreview:  DefaultIdlePreviewTTLMinutes,
			expectedPreviewHolds: true,
		},
		{
			name:                 "custom values pass through",
			raw:                  json.RawMessage(`{"sandbox_lifecycle":{"completed_session_retention_minutes":120,"idle_preview_ttl_minutes":300,"preview_holds_sandbox":false}}`),
			expectedRetention:    120,
			expectedIdlePreview:  300,
			expectedPreviewHolds: false,
		},
		{
			name:                 "values clamp to supported bounds",
			raw:                  json.RawMessage(`{"sandbox_lifecycle":{"completed_session_retention_minutes":-1,"idle_preview_ttl_minutes":99999}}`),
			expectedRetention:    MinCompletedSessionRetentionMinutes,
			expectedIdlePreview:  MaxIdlePreviewTTLMinutes,
			expectedPreviewHolds: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := ParseOrgSettings(tt.raw)
			require.NoError(t, err, "ParseOrgSettings should accept sandbox lifecycle settings")
			require.Equal(t, tt.expectedRetention, s.SandboxLifecycle.CompletedSessionRetentionMinutes, "completed-session retention should be normalized")
			require.Equal(t, tt.expectedIdlePreview, s.SandboxLifecycle.IdlePreviewTTLMinutes, "idle-preview ttl should be normalized")
			require.Equal(t, tt.expectedPreviewHolds, s.SandboxLifecycle.EffectivePreviewHoldsSandbox(), "preview hold policy should be effective")
		})
	}
}

func TestParseOrgSettings_SandboxResourceDefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sandbox_resources":{"agent_default_tier":"large","preview_default_tier":"small","allow_repo_resource_requests":false,"preview_max_tier":"standard","preview_max_cpu_millis":1500,"preview_max_memory_mib":4096,"preview_max_ephemeral_disk_mib":6144}}`)

	s, err := ParseOrgSettings(raw)
	require.NoError(t, err, "ParseOrgSettings should accept sandbox resource settings")
	require.Equal(t, SandboxResourceTierLarge, s.SandboxResources.AgentDefaultTier, "agent default tier should pass through")
	require.Equal(t, SandboxResourceTierSmall, s.SandboxResources.PreviewDefaultTier, "preview default tier should pass through")
	require.False(t, s.SandboxResources.EffectiveAllowRepoResourceRequests(), "explicit false should be preserved")
	require.Equal(t, SandboxResourceTierStandard, s.SandboxResources.PreviewMaxTier, "preview max tier should pass through")
	require.Equal(t, 1500, s.SandboxResources.PreviewMaxCPUMillis, "preview CPU max should pass through")
	require.Equal(t, 4096, s.SandboxResources.PreviewMaxMemoryMiB, "preview memory max should pass through")
	require.Equal(t, 6144, s.SandboxResources.PreviewMaxEphemeralDiskMiB, "preview disk max should pass through")
}

func TestParseOrgSettings_SandboxResourceLimitDefaultsAndClamps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		raw               json.RawMessage
		expectedCPUMillis int
		expectedMemoryMiB int
		expectedDiskMiB   int
	}{
		{
			name:              "missing values default to platform caps",
			raw:               json.RawMessage(`{}`),
			expectedCPUMillis: DefaultPreviewMaxCPUMillis,
			expectedMemoryMiB: DefaultPreviewMaxMemoryMiB,
			expectedDiskMiB:   DefaultPreviewMaxEphemeralDiskMiB,
		},
		{
			name:              "zero values default to platform caps",
			raw:               json.RawMessage(`{"sandbox_resources":{"preview_max_cpu_millis":0,"preview_max_memory_mib":0,"preview_max_ephemeral_disk_mib":0}}`),
			expectedCPUMillis: DefaultPreviewMaxCPUMillis,
			expectedMemoryMiB: DefaultPreviewMaxMemoryMiB,
			expectedDiskMiB:   DefaultPreviewMaxEphemeralDiskMiB,
		},
		{
			name:              "below minimum clamps up",
			raw:               json.RawMessage(`{"sandbox_resources":{"preview_max_cpu_millis":-1,"preview_max_memory_mib":-1,"preview_max_ephemeral_disk_mib":-1}}`),
			expectedCPUMillis: MinPreviewMaxCPUMillis,
			expectedMemoryMiB: MinPreviewMaxMemoryMiB,
			expectedDiskMiB:   MinPreviewMaxEphemeralDiskMiB,
		},
		{
			name:              "above maximum clamps down",
			raw:               json.RawMessage(`{"sandbox_resources":{"preview_max_cpu_millis":99999,"preview_max_memory_mib":99999,"preview_max_ephemeral_disk_mib":99999}}`),
			expectedCPUMillis: MaxPreviewMaxCPUMillis,
			expectedMemoryMiB: MaxPreviewMaxMemoryMiB,
			expectedDiskMiB:   MaxPreviewMaxEphemeralDiskMiB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := ParseOrgSettings(tt.raw)
			require.NoError(t, err, "ParseOrgSettings should normalize preview resource caps")
			require.Equal(t, tt.expectedCPUMillis, s.SandboxResources.PreviewMaxCPUMillis, "preview CPU max should be normalized")
			require.Equal(t, tt.expectedMemoryMiB, s.SandboxResources.PreviewMaxMemoryMiB, "preview memory max should be normalized")
			require.Equal(t, tt.expectedDiskMiB, s.SandboxResources.PreviewMaxEphemeralDiskMiB, "preview disk max should be normalized")
		})
	}
}

func TestSandboxResourceTierValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tier    SandboxResourceTier
		wantErr bool
	}{
		{name: "empty valid", tier: "", wantErr: false},
		{name: "small valid", tier: SandboxResourceTierSmall, wantErr: false},
		{name: "standard valid", tier: SandboxResourceTierStandard, wantErr: false},
		{name: "large valid", tier: SandboxResourceTierLarge, wantErr: false},
		{name: "invalid", tier: SandboxResourceTier("xlarge"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.tier.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid sandbox resource tiers")
				return
			}
			require.NoError(t, err, "Validate should accept known sandbox resource tiers")
		})
	}
}

func TestParseOrgSettings_RuntimeBudgets_Defaults(t *testing.T) {
	t.Parallel()

	s, err := ParseOrgSettings(nil)
	require.NoError(t, err, "ParseOrgSettings should apply runtime budget defaults")
	require.Equal(t, DefaultNoProgressTimeoutSeconds, s.RuntimeBudgets.NoProgressTimeoutSeconds, "no-progress timeout should default")
	require.Equal(t, 15*60, s.RuntimeBudgets.NoProgressTimeoutSeconds, "no-progress timeout should default to fifteen minutes")
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
