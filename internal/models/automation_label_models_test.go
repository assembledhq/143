package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomationLabelModelValidation(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"label_model_overrides":{"ready":"gpt-5.6-sol"}}`,
		`{"check_run":{"name":"policy","app_id":1},"labels":["ready"],"label_model_overrides":{"ready":""}}`,
		`{"check_run":{"name":"policy","app_id":1},"labels":["ready"],"label_model_overrides":{"ready":"x","READY":"x"}}`,
		`{"check_run":{"name":"policy","app_id":1},"labels":["ready","other"],"label_model_overrides":{"ready":"x"}}`,
		`{"check_run":{"name":"policy","app_id":1},"labels":["ready"],"label_model_overrides":{"other":"x"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			var f AutomationGitHubEventFilters
			require.NoError(t, json.Unmarshal([]byte(raw), &f), "fixture should decode")
			require.Error(t, f.Validate(), "invalid mapping must fail closed")
		})
	}
}

func TestAutomationFrozenLabelModel(t *testing.T) {
	t.Parallel()
	agent := string(AgentTypeCodex)
	a := Automation{AgentType: &agent, GitHubEventFilters: json.RawMessage(`{"check_run":{"name":"policy","app_id":1},"labels":["ready"],"label_model_overrides":{"ready":"gpt-5.6-sol"}}`)}
	var f AutomationGitHubEventFilters
	require.NoError(t, json.Unmarshal(a.GitHubEventFilters, &f), "decode mapping")
	selected, evidence, err := a.SelectLabelModel(f, []string{"READY"})
	require.NoError(t, err, "case-insensitive selection should succeed")
	require.Equal(t, "gpt-5.6-sol", *selected.ModelOverride, "mapping should select configured model")
	snapshot, err := json.Marshal(map[string]any{"label_model_selection": evidence})
	require.NoError(t, err, "marshal frozen selection")
	changedAgent := string(AgentTypeClaudeCode)
	frozen, err := AutomationWithRunLabelModel(Automation{AgentType: &changedAgent}, AutomationRun{ConfigSnapshot: snapshot})
	require.NoError(t, err, "later configuration must not replace frozen selection")
	require.Equal(t, agent, *frozen.AgentType, "frozen agent should control execution")
	require.Equal(t, "gpt-5.6-sol", *frozen.ModelOverride, "frozen model should control execution")
	_, _, err = a.SelectLabelModel(f, nil)
	require.Error(t, err, "unmatched labels must not fall back")
	_, err = AutomationWithRunLabelModel(a, AutomationRun{ConfigSnapshot: json.RawMessage(`{"label_model_selection":{"label":"ready","labels":["ready"],"agent_type":"codex","model":"unknown"}}`)})
	require.Error(t, err, "model removed from catalog must fail closed")
}

func TestAutomationExplicitRoutesValidateAndFreezeReasoning(t *testing.T) {
	t.Parallel()
	base := AutomationGitHubEventFilters{CheckRun: &AutomationGitHubCheckRunFilter{Name: "policy", AppID: 1}, Labels: []string{"one", "two"}, LabelModelRoutes: map[string]AutomationLabelModelRoute{"one": {AgentType: AgentTypeCodex, Model: CodexModelGPT6Astra}, "two": {AgentType: AgentTypeClaudeCode, Model: ClaudeCodeModelFable5}}}
	for _, tt := range []struct {
		name   string
		change func(*AutomationGitHubEventFilters, *Automation)
		bad    bool
	}{
		{name: "valid"},
		{name: "mutually exclusive", bad: true, change: func(f *AutomationGitHubEventFilters, _ *Automation) {
			f.LabelModelOverrides = map[string]string{"one": CodexModelGPT6Astra}
		}},
		{name: "wrong provider", bad: true, change: func(f *AutomationGitHubEventFilters, _ *Automation) {
			f.LabelModelRoutes["one"] = AutomationLabelModelRoute{AgentType: AgentTypeClaudeCode, Model: CodexModelGPT6Astra}
		}},
		{name: "case collision", bad: true, change: func(f *AutomationGitHubEventFilters, _ *Automation) {
			f.LabelModelRoutes["ONE"] = f.LabelModelRoutes["one"]
		}},
		{name: "missing coverage", bad: true, change: func(f *AutomationGitHubEventFilters, _ *Automation) { delete(f.LabelModelRoutes, "two") }},
		{name: "unknown provider", bad: true, change: func(f *AutomationGitHubEventFilters, _ *Automation) {
			f.LabelModelRoutes["one"] = AutomationLabelModelRoute{AgentType: "unknown", Model: CodexModelGPT6Astra}
		}},
		{name: "incompatible shared effort", bad: true, change: func(_ *AutomationGitHubEventFilters, a *Automation) {
			effort := ReasoningEffortMax
			a.ReasoningEffort = &effort
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(base)
			require.NoError(t, err, "copy filters")
			var f AutomationGitHubEventFilters
			require.NoError(t, json.Unmarshal(raw, &f), "copy routes")
			a := Automation{}
			if tt.change != nil {
				tt.change(&f, &a)
			}
			a.GitHubEventFilters, err = json.Marshal(f)
			require.NoError(t, err, "encode routes")
			if tt.bad {
				require.Error(t, a.ValidateLabelModelOverrides(), "invalid route config rejected")
			} else {
				require.NoError(t, a.ValidateLabelModelOverrides(), "explicit routes need no single outer agent")
			}
		})
	}
	raw, err := json.Marshal(base)
	require.NoError(t, err, "encode valid routes")
	a := Automation{GitHubEventFilters: raw}
	_, selected, err := a.SelectLabelModel(base, []string{"one"})
	require.NoError(t, err, "select explicit route")
	snapshot, err := json.Marshal(map[string]any{"label_model_selection": selected})
	require.NoError(t, err, "encode selected route")
	max := ReasoningEffortMax
	a.ReasoningEffort = &max
	frozen, err := AutomationWithRunLabelModel(a, AutomationRun{ConfigSnapshot: snapshot})
	require.NoError(t, err, "later shared effort must not replace frozen default")
	require.Nil(t, frozen.ReasoningEffort, "selected default reasoning should remain default")
}
