package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateAutomationScheduleType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     AutomationScheduleType
		expectErr bool
	}{
		{name: "interval is valid", input: AutomationScheduleInterval},
		{name: "cron is valid", input: AutomationScheduleCron},
		{name: "none is valid", input: AutomationScheduleNone},
		{name: "empty is invalid", input: "", expectErr: true},
		{name: "garbage is invalid", input: "every-5-minutes", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAutomationScheduleType(string(tt.input))
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAutomationIdentityScopeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scope     AutomationIdentityScope
		expectErr bool
	}{
		{name: "org is valid", scope: AutomationIdentityScopeOrg},
		{name: "personal is valid", scope: AutomationIdentityScopePersonal},
		{name: "empty defaults valid", scope: ""},
		{name: "invalid", scope: "team", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.scope.Validate()
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestAutomationProductTriggerValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		trigger   AutomationProductTrigger
		expectErr bool
	}{
		{name: "pr opened", trigger: AutomationProductTriggerPROpened},
		{name: "pr ready for review", trigger: AutomationProductTriggerPRReadyForReview},
		{name: "checks completed", trigger: AutomationProductTriggerChecksCompleted},
		{name: "raw github event is not a valid product trigger", trigger: AutomationProductTrigger("github.check_suite.completed"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.trigger.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid product trigger should fail validation")
				return
			}
			require.NoError(t, err, "known product trigger should pass validation")
		})
	}
}

func TestValidateCronExpression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		expr      string
		expectErr bool
	}{
		{name: "5-field daily", expr: "0 9 * * *"},
		{name: "5-field weekly", expr: "0 9 * * 1"},
		{name: "6-field with seconds", expr: "0 0 9 * * *"},
		{name: "alias", expr: "@daily"},
		{name: "empty", expr: "", expectErr: true},
		{name: "garbage", expr: "every monday", expectErr: true},
		{name: "too few fields", expr: "0 9 *", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCronExpression(tt.expr)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateIntervalRunAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expectErr bool
	}{
		{name: "valid five minute boundary", input: "09:35"},
		{name: "invalid format", input: "9:35", expectErr: true},
		{name: "invalid parse with correct length", input: "ab:cd", expectErr: true},
		{name: "invalid minute step", input: "09:37", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateIntervalRunAt(tt.input)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestNextCronRunTime(t *testing.T) {
	t.Parallel()

	// 9am daily, evaluated from 8am UTC → next fire is 9am UTC same day.
	from := time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)
	next, err := NextCronRunTime("0 9 * * *", "UTC", from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), next)

	// Same cron in America/New_York: 9am ET on 2026-04-17 = 13:00 UTC (EDT).
	next, err = NextCronRunTime("0 9 * * *", "America/New_York", from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 13, 0, 0, 0, time.UTC), next)

	// Unknown timezone.
	_, err = NextCronRunTime("0 9 * * *", "Mars/Olympus", from)
	require.Error(t, err)

	// Malformed expression.
	_, err = NextCronRunTime("not a cron", "UTC", from)
	require.Error(t, err)
}

func TestComputeNextRunAt(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, 4, 17, 8, 0, 0, 0, time.UTC)

	iv := 6
	iu := ScheduleUnitHours
	interval := Automation{
		ScheduleType:  AutomationScheduleInterval,
		IntervalValue: &iv,
		IntervalUnit:  &iu,
	}
	got, err := interval.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, from.Add(6*time.Hour), got)

	runAt := "11:15"
	interval.IntervalRunAt = &runAt
	got, err = interval.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 14, 15, 0, 0, time.UTC), got)

	invalidRunAt := "ab:cd"
	interval.IntervalRunAt = &invalidRunAt
	_, err = interval.ComputeNextRunAt(from)
	require.Error(t, err)

	// Interval with missing companion fields is rejected (corrupt row).
	bad := Automation{ScheduleType: AutomationScheduleInterval}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	expr := "0 9 * * *"
	cron := Automation{
		ScheduleType:   AutomationScheduleCron,
		CronExpression: &expr,
		Timezone:       "UTC",
	}
	got, err = cron.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), got)

	// Cron with missing expression is rejected.
	bad = Automation{ScheduleType: AutomationScheduleCron}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	// Unknown schedule kind is rejected.
	bad = Automation{ScheduleType: "event"}
	_, err = bad.ComputeNextRunAt(from)
	require.Error(t, err)

	// Empty timezone on a cron schedule defaults to UTC so legacy rows
	// imported without an explicit zone still fire correctly.
	cronNoTz := Automation{
		ScheduleType:   AutomationScheduleCron,
		CronExpression: &expr,
	}
	got, err = cronNoTz.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC), got)

	none := Automation{ScheduleType: AutomationScheduleNone}
	got, err = none.ComputeNextRunAt(from)
	require.NoError(t, err)
	require.True(t, got.IsZero(), "schedule_type=none should not compute a next scheduled run")
}

func TestValidateAutomationRunStatus(t *testing.T) {
	t.Parallel()

	valid := []AutomationRunStatus{
		AutomationRunStatusPending,
		AutomationRunStatusRunning,
		AutomationRunStatusCompleted,
		AutomationRunStatusCompletedNoop,
		AutomationRunStatusFailed,
		AutomationRunStatusSkipped,
	}
	for _, s := range valid {
		t.Run("valid_"+string(s), func(t *testing.T) {
			t.Parallel()
			require.NoError(t, ValidateAutomationRunStatus(string(s)))
		})
	}

	invalid := []string{"", "queued", "canceled", "PENDING"}
	for _, s := range invalid {
		t.Run("invalid_"+s, func(t *testing.T) {
			t.Parallel()
			require.Error(t, ValidateAutomationRunStatus(s))
		})
	}
}

func TestAutomationIconTypeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		icon    AutomationIconType
		wantErr bool
	}{
		{name: "empty uses default", icon: "", wantErr: false},
		{name: "emoji is supported", icon: AutomationIconTypeEmoji, wantErr: false},
		{name: "image is not enabled yet", icon: AutomationIconType("image"), wantErr: true},
		{name: "unknown is rejected", icon: AutomationIconType("color"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.icon.Validate()
			if tt.wantErr {
				require.Error(t, err, "unsupported automation icon types should be rejected")
				return
			}
			require.NoError(t, err, "supported automation icon types should validate")
		})
	}
}

func TestBuildConfigSnapshot(t *testing.T) {
	t.Parallel()

	agent := "codex"
	model := "opus-4-7"
	scope := "src/"
	reasoning := ReasoningEffortXHigh
	lastRunAt := time.Date(2026, 6, 27, 9, 30, 0, 0, time.FixedZone("test", -4*60*60))
	a := Automation{
		AgentType:        &agent,
		ModelOverride:    &model,
		ReasoningEffort:  &reasoning,
		Scope:            &scope,
		IdentityScope:    AutomationIdentityScopePersonal,
		PublishPolicy:    AutomationPublishPolicyNone,
		PrePRReviewLoops: 2,
		BaseBranch:       "main",
		LastRunAt:        &lastRunAt,
		FallbackModels:   AutomationFallbackModels{Models: []string{CodexModelGPT55}},
	}

	raw, err := a.BuildConfigSnapshot()
	require.NoError(t, err, "BuildConfigSnapshot should marshal automation config")
	require.NotEmpty(t, raw, "config snapshot should not be empty")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "config snapshot should be valid JSON")
	require.Equal(t, "codex", decoded["agent_type"], "config snapshot should include agent type")
	require.Equal(t, "opus-4-7", decoded["model_override"], "config snapshot should include model override")
	require.Equal(t, "xhigh", decoded["reasoning_effort"], "config snapshot should include reasoning effort")
	require.Equal(t, "src/", decoded["scope"], "config snapshot should include scope")
	require.Equal(t, string(AutomationIdentityScopePersonal), decoded["identity_scope"], "config snapshot should include identity scope")
	require.Equal(t, string(AutomationPublishPolicyNone), decoded["publish_policy"], "config snapshot should include the publish policy")
	require.Equal(t, float64(2), decoded["pre_pr_review_loops"], "config snapshot should include the pre-PR review pass count")
	require.Equal(t, "main", decoded["base_branch"], "config snapshot should include base branch")
	require.Equal(t, "2026-06-27T13:30:00Z", decoded["previous_run_at"], "config snapshot should include the previous automation run time in UTC")
	require.Equal(t, map[string]any{"models": []any{CodexModelGPT55}}, decoded["fallback_models"],
		"config snapshot should freeze the fallback chain the run dispatches under")
}

func TestBuildConfigSnapshot_NilOptionalFields(t *testing.T) {
	t.Parallel()

	a := Automation{BaseBranch: "develop"}

	raw, err := a.BuildConfigSnapshot()
	require.NoError(t, err, "BuildConfigSnapshot should marshal automation config with nil optional fields")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "config snapshot should be valid JSON")
	require.Nil(t, decoded["agent_type"], "config snapshot should preserve nil agent type")
	require.Nil(t, decoded["model_override"], "config snapshot should preserve nil model override")
	require.Nil(t, decoded["reasoning_effort"], "config snapshot should preserve nil reasoning effort")
	require.Nil(t, decoded["scope"], "config snapshot should preserve nil scope")
	require.Equal(t, string(AutomationIdentityScopeOrg), decoded["identity_scope"], "config snapshot should default identity scope")
	require.Equal(t, string(AutomationPublishPolicyPullRequest), decoded["publish_policy"], "config snapshot should default to pull request publication")
	require.Equal(t, float64(0), decoded["pre_pr_review_loops"], "config snapshot should include disabled pre-PR review by default")
	require.Equal(t, "develop", decoded["base_branch"], "config snapshot should include base branch")
	require.Nil(t, decoded["previous_run_at"], "config snapshot should preserve missing previous automation run time")
	// Present-but-empty rather than absent: the key is what tells a reader the
	// snapshot is new enough to be authoritative about the chain, so an
	// automation with no fallbacks still has to emit it.
	require.Equal(t, map[string]any{}, decoded["fallback_models"], "config snapshot should include an empty fallback chain")
}

func TestAutomationPublishPolicyValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  AutomationPublishPolicy
		wantErr bool
	}{
		{name: "pull request", policy: AutomationPublishPolicyPullRequest},
		{name: "none", policy: AutomationPublishPolicyNone},
		{name: "branch is unsupported", policy: AutomationPublishPolicy("branch"), wantErr: true},
		{name: "empty is invalid when explicitly provided", policy: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.policy.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid publish policy should be rejected")
				return
			}
			require.NoError(t, err, "supported publish policy should be accepted")
		})
	}
}

func TestAutomationPublishPolicyFromConfigSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      json.RawMessage
		expected AutomationPublishPolicy
		wantErr  bool
	}{
		{name: "pull request", raw: json.RawMessage(`{"publish_policy":"pull_request"}`), expected: AutomationPublishPolicyPullRequest},
		{name: "none", raw: json.RawMessage(`{"publish_policy":"none"}`), expected: AutomationPublishPolicyNone},
		{name: "legacy snapshot", raw: json.RawMessage(`{}`), expected: AutomationPublishPolicyPullRequest},
		{name: "missing snapshot", expected: AutomationPublishPolicyPullRequest},
		{name: "branch is unsupported", raw: json.RawMessage(`{"publish_policy":"branch"}`), wantErr: true},
		{name: "malformed snapshot", raw: json.RawMessage(`{`), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := AutomationPublishPolicyFromConfigSnapshot(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "invalid publish policy snapshot should be rejected")
				return
			}
			require.NoError(t, err, "valid publish policy snapshot should resolve")
			require.Equal(t, tt.expected, actual, "snapshot should resolve to the expected publish policy")
		})
	}
}

func TestAutomationGitHubEventValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		event   AutomationGitHubEvent
		wantErr bool
	}{
		{name: "pull request opened", event: AutomationGitHubEventPullRequestOpened},
		{name: "pull request updated", event: AutomationGitHubEventPullRequestUpdated},
		{name: "pull request ready for review", event: AutomationGitHubEventPullRequestReadyForReview},
		{name: "pull request merged", event: AutomationGitHubEventPullRequestMerged},
		{name: "check suite completed", event: AutomationGitHubEventCheckSuiteCompleted},
		{name: "check run completed", event: AutomationGitHubEventCheckRunCompleted},
		{name: "issue comment created", event: AutomationGitHubEventIssueCommentCreated},
		{name: "pull request review submitted", event: AutomationGitHubEventPullRequestReviewSubmitted},
		{name: "pull request review comment created", event: AutomationGitHubEventPullRequestReviewCommentCreated},
		{name: "empty", event: AutomationGitHubEvent(""), wantErr: true},
		{name: "unknown", event: AutomationGitHubEvent("github.unknown"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.event.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid GitHub automation event should fail validation")
				return
			}
			require.NoError(t, err, "supported GitHub automation event should validate")
		})
	}
}

func TestAutomationFallbackModelsValidate(t *testing.T) {
	t.Parallel()

	claudeAgent := "claude_code"
	codexAgent := "codex"
	ampAgent := "amp"
	maxEffort := ReasoningEffortMax

	tests := []struct {
		name      string
		fallbacks AutomationFallbackModels
		agentType *string
		effort    *ReasoningEffort
		wantErr   string
	}{
		{
			name:      "empty is valid",
			fallbacks: AutomationFallbackModels{},
		},
		{
			name:      "models without explicit agents infer from the model name",
			fallbacks: AutomationFallbackModels{Models: []string{CodexModelGPT55, ClaudeCodeModelSonnet46}},
			agentType: &claudeAgent,
		},
		{
			name:      "explicit cross-agent fallback is allowed",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{codexAgent}, Models: []string{CodexModelGPT55}},
			agentType: &claudeAgent,
		},
		{
			name:      "duplicates are accepted",
			fallbacks: AutomationFallbackModels{Models: []string{CodexModelGPT55, CodexModelGPT55, CodexModelGPT55, CodexModelGPT55}},
			agentType: &codexAgent,
		},
		{
			name: "more than the cap is rejected",
			fallbacks: AutomationFallbackModels{Models: []string{
				CodexModelGPT55, CodexModelGPT54, CodexModelGPT53Codex, CodexModelGPT52Codex, CodexModelGPT5Codex,
			}},
			agentType: &codexAgent,
			wantErr:   "at most 4 fallback models are allowed",
		},
		{
			name:      "agent types must match the model count",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{codexAgent, codexAgent}, Models: []string{CodexModelGPT55}},
			agentType: &codexAgent,
			wantErr:   "agent types must match",
		},
		{
			name:      "reasoning efforts must match the model count",
			fallbacks: AutomationFallbackModels{Models: []string{CodexModelGPT55}, ReasoningEfforts: []ReasoningEffort{ReasoningEffortHigh, ReasoningEffortLow}},
			agentType: &codexAgent,
			wantErr:   "reasoning efforts must match",
		},
		{
			name:      "arrays without models are rejected",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{codexAgent}},
			agentType: &codexAgent,
			wantErr:   "require a fallback model list",
		},
		{
			name:      "whitespace-only model is rejected",
			fallbacks: AutomationFallbackModels{Models: []string{"   "}},
			agentType: &codexAgent,
			wantErr:   "fallback model 1 must be non-empty",
		},
		{
			name:      "model illegal for its resolved agent is rejected",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{codexAgent}, Models: []string{ClaudeCodeModelOpus5}},
			agentType: &codexAgent,
			wantErr:   "invalid fallback model 1",
		},
		{
			// Inheritance is a convenience, not a constraint: a Claude-Code
			// primary at "max" must not make every Codex fallback unsavable.
			name:      "an effort the rank's agent cannot run is not inherited",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{codexAgent}, Models: []string{CodexModelGPT55}},
			agentType: &claudeAgent,
			effort:    &maxEffort,
		},
		{
			name:      "an agent with no reasoning levels at all can still be a fallback",
			fallbacks: AutomationFallbackModels{AgentTypes: []string{ampAgent}, Models: []string{AmpModeSmart}},
			agentType: &claudeAgent,
			effort:    &maxEffort,
		},
		{
			// An explicit value is the user's own typo, so it still errors
			// rather than being silently downgraded.
			name: "an explicit effort the rank's agent cannot run is rejected",
			fallbacks: AutomationFallbackModels{
				AgentTypes:       []string{codexAgent},
				Models:           []string{CodexModelGPT55},
				ReasoningEfforts: []ReasoningEffort{ReasoningEffortMax},
			},
			agentType: &claudeAgent,
			wantErr:   `reasoning effort "max" is not supported for fallback model 1`,
		},
		{
			name: "an explicit per-rank effort overrides an un-inheritable one",
			fallbacks: AutomationFallbackModels{
				AgentTypes:       []string{codexAgent},
				Models:           []string{CodexModelGPT55},
				ReasoningEfforts: []ReasoningEffort{ReasoningEffortHigh},
			},
			agentType: &claudeAgent,
			effort:    &maxEffort,
		},
		{
			name:      "unrecognized model with no agent to fall back on is rejected",
			fallbacks: AutomationFallbackModels{Models: []string{"not-a-real-model"}},
			wantErr:   "is not recognized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.fallbacks.Validate(tt.agentType, tt.effort)
			if tt.wantErr == "" {
				require.NoError(t, err, "fallback chain should validate")
				return
			}
			require.Error(t, err, "invalid fallback chain should be rejected")
			require.Contains(t, err.Error(), tt.wantErr, "error should explain which rank failed")
			require.Contains(t, err.Error(), "model", "message must mention model so handlers classify it as INVALID_MODEL")
		})
	}
}

func TestAutomationFallbackModelsNormalize(t *testing.T) {
	t.Parallel()

	t.Run("drops arrays that carry no information", func(t *testing.T) {
		t.Parallel()

		normalized := AutomationFallbackModels{
			AgentTypes:       []string{"", ""},
			Models:           []string{"  " + CodexModelGPT55 + "  ", CodexModelGPT54},
			ReasoningEfforts: []ReasoningEffort{"", ""},
		}.Normalize()

		require.Equal(t, []string{CodexModelGPT55, CodexModelGPT54}, normalized.Models, "models should be trimmed")
		require.Nil(t, normalized.AgentTypes, "an all-empty agent list should normalize away")
		require.Nil(t, normalized.ReasoningEfforts, "an all-empty effort list should normalize away")
	})

	t.Run("an empty model list normalizes to the zero value", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, AutomationFallbackModels{}, AutomationFallbackModels{Models: []string{}}.Normalize(),
			"no fallbacks must round-trip as the zero value so no-op patches produce no audit diff")
	})

	t.Run("pads a partially specified array to the model count", func(t *testing.T) {
		t.Parallel()

		normalized := AutomationFallbackModels{
			Models:           []string{CodexModelGPT55, CodexModelGPT54},
			ReasoningEfforts: []ReasoningEffort{ReasoningEffortHigh},
		}.Normalize()

		require.Len(t, normalized.ReasoningEfforts, 2, "index-aligned arrays must match the model count")
		require.Equal(t, ReasoningEffortHigh, normalized.ReasoningEfforts[0], "specified entries should survive")
		require.Equal(t, ReasoningEffort(""), normalized.ReasoningEfforts[1], "unspecified entries pad as empty")
	})
}

func TestAutomationModelRanks(t *testing.T) {
	t.Parallel()

	claudeAgent := "claude_code"
	codexAgent := "codex"
	primaryModel := ClaudeCodeModelOpus5
	highEffort := ReasoningEffortHigh

	t.Run("an automation with no fallbacks yields only the primary", func(t *testing.T) {
		t.Parallel()

		a := Automation{AgentType: &claudeAgent, ModelOverride: &primaryModel, ReasoningEffort: &highEffort}
		ranks := a.ModelRanks()

		require.Len(t, ranks, 1, "a legacy automation has exactly one rank")
		require.False(t, ranks[0].Fallback, "rank 0 is the configured primary, not a fallback")
		require.Equal(t, primaryModel, *ranks[0].Model, "rank 0 carries the automation's model override")
		require.Equal(t, highEffort, *ranks[0].ReasoningEffort, "rank 0 carries the automation's reasoning effort")
	})

	t.Run("fallbacks resolve their agent and inherit the primary effort", func(t *testing.T) {
		t.Parallel()

		a := Automation{
			AgentType:       &claudeAgent,
			ModelOverride:   &primaryModel,
			ReasoningEffort: &highEffort,
			FallbackModels: AutomationFallbackModels{
				Models: []string{ClaudeCodeModelSonnet46, CodexModelGPT55},
			},
		}
		ranks := a.ModelRanks()

		require.Len(t, ranks, 3, "one primary plus two fallbacks")
		require.Equal(t, claudeAgent, *ranks[1].AgentType, "a same-agent fallback keeps the primary agent")
		require.Equal(t, codexAgent, *ranks[2].AgentType, "a fallback's agent is inferred from its model name")
		require.True(t, ranks[1].Fallback, "ranks beyond the primary are fallbacks")
		require.Equal(t, highEffort, *ranks[1].ReasoningEffort, "a fallback with no explicit effort inherits the primary's")
	})

	t.Run("a rank whose agent cannot run the primary effort does not inherit it", func(t *testing.T) {
		t.Parallel()

		maxEffort := ReasoningEffortMax
		a := Automation{
			AgentType:       &claudeAgent,
			ModelOverride:   &primaryModel,
			ReasoningEffort: &maxEffort,
			FallbackModels:  AutomationFallbackModels{Models: []string{CodexModelGPT55}},
		}
		ranks := a.ModelRanks()

		require.Equal(t, maxEffort, *ranks[0].ReasoningEffort, "the primary keeps its own effort")
		require.Nil(t, ranks[1].ReasoningEffort, "codex has no max level, so the fallback runs at its agent default")
	})

	t.Run("an explicit per-rank effort wins over the primary", func(t *testing.T) {
		t.Parallel()

		a := Automation{
			AgentType:       &claudeAgent,
			ModelOverride:   &primaryModel,
			ReasoningEffort: &highEffort,
			FallbackModels: AutomationFallbackModels{
				AgentTypes:       []string{codexAgent},
				Models:           []string{CodexModelGPT55},
				ReasoningEfforts: []ReasoningEffort{ReasoningEffortLow},
			},
		}
		ranks := a.ModelRanks()

		require.Equal(t, ReasoningEffortLow, *ranks[1].ReasoningEffort, "an explicit rank effort overrides the primary")
		require.Equal(t, codexAgent, *ranks[1].AgentType, "an explicit rank agent overrides inference")
	})
}

func TestAutomationModelRanksFromConfigSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("a snapshot predating fallback models reports not-ok", func(t *testing.T) {
		t.Parallel()

		ranks, ok, err := AutomationModelRanksFromConfigSnapshot(json.RawMessage(`{"agent_type":"codex","model_override":"gpt-5.5"}`))

		require.NoError(t, err, "a legacy snapshot is not an error")
		require.False(t, ok, "an absent fallback_models key means the caller should read the live automation")
		require.Nil(t, ranks, "no chain is reconstructed from a legacy snapshot")
	})

	t.Run("an empty snapshot reports not-ok", func(t *testing.T) {
		t.Parallel()

		_, ok, err := AutomationModelRanksFromConfigSnapshot(nil)

		require.NoError(t, err, "a missing snapshot is not an error")
		require.False(t, ok, "there is no chain to reconstruct")
	})

	t.Run("round-trips the chain a run was dispatched under", func(t *testing.T) {
		t.Parallel()

		claudeAgent := "claude_code"
		model := ClaudeCodeModelOpus5
		effort := ReasoningEffortHigh
		a := Automation{
			AgentType:       &claudeAgent,
			ModelOverride:   &model,
			ReasoningEffort: &effort,
			BaseBranch:      "main",
			FallbackModels:  AutomationFallbackModels{Models: []string{CodexModelGPT55}},
		}
		snapshot, err := a.BuildConfigSnapshot()
		require.NoError(t, err, "snapshot should marshal")

		ranks, ok, err := AutomationModelRanksFromConfigSnapshot(snapshot)

		require.NoError(t, err, "snapshot should decode")
		require.True(t, ok, "a snapshot carrying fallback_models has an authoritative chain")
		require.Equal(t, a.ModelRanks(), ranks, "the frozen chain must match what the automation had at dispatch")
	})

	t.Run("an automation edited after the run cannot re-rank it", func(t *testing.T) {
		t.Parallel()

		claudeAgent := "claude_code"
		model := ClaudeCodeModelOpus5
		a := Automation{AgentType: &claudeAgent, ModelOverride: &model}
		snapshot, err := a.BuildConfigSnapshot()
		require.NoError(t, err, "snapshot should marshal")

		a.FallbackModels = AutomationFallbackModels{Models: []string{CodexModelGPT55}}

		ranks, ok, err := AutomationModelRanksFromConfigSnapshot(snapshot)

		require.NoError(t, err, "snapshot should decode")
		require.True(t, ok, "the snapshot is authoritative")
		require.Len(t, ranks, 1, "a fallback added after dispatch must not appear in the run's chain")
	})

	t.Run("malformed JSON surfaces an error", func(t *testing.T) {
		t.Parallel()

		_, _, err := AutomationModelRanksFromConfigSnapshot(json.RawMessage(`{`))

		require.Error(t, err, "a corrupt snapshot must not silently read as no-chain")
	})
}
