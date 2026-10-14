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
