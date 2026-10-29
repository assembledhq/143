package automations

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTargetDispatcherFrozenLabelModelRetiresDifferentModel(t *testing.T) {
	t.Parallel()
	oldModel, newModel := models.CodexModelGPT56Sol, models.ClaudeCodeModelFable5
	agent := string(models.AgentTypeCodex)
	current := models.Automation{AgentType: &agent, ModelOverride: &oldModel}
	snapshot, err := json.Marshal(map[string]any{"label_model_selection": models.AutomationLabelModelSelection{Label: "review-deep", Model: newModel, AgentType: models.AgentTypeClaudeCode, Labels: []string{"review-deep"}}})
	require.NoError(t, err, "marshal run selection")
	frozen, err := models.AutomationWithRunLabelModel(current, models.AutomationRun{ConfigSnapshot: snapshot})
	require.NoError(t, err, "reread automation should receive frozen model")
	d := NewTargetDispatcher(nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	decision := d.decide(frozen, &models.Session{AgentType: models.AgentTypeClaudeCode}, nil, automationRunGitHubContext{}, models.AutomationTargetSession{}, true, models.Session{ID: uuid.New(), AgentType: models.AgentTypeCodex, ModelOverride: &oldModel, Status: models.SessionStatusIdle}, false)
	require.Equal(t, retireDecision(models.AutomationTargetRetiredAgentConfigChanged), decision, "different selected model must not resume the old model session")
	require.Equal(t, newModel, *frozen.ModelOverride, "fresh execution should retain the selected model")
	require.Equal(t, oldModel, *current.ModelOverride, "run selection must not mutate current configuration")
}
