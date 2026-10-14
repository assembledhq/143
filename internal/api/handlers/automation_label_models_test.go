package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestAutomationLabelModelsAPIValidation(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"check_run":{"name":"policy","app_id":1},"labels":[" REVIEW-DEEP "],"label_model_overrides":{"review-deep":"gpt-5.6-sol"}}`)
	normalized, err := validateAutomationGitHubEventFilters(raw)
	require.NoError(t, err, "normalized eligibility should match case-insensitive mapping")
	agent := string(models.AgentTypeCodex)
	a := models.Automation{AgentType: &agent, GitHubEventFilters: normalized}
	require.NoError(t, a.ValidateLabelModelOverrides(), "recognized model should bind to configured agent")
	wrongAgent := string(models.AgentTypeClaudeCode)
	a.AgentType = &wrongAgent
	require.Error(t, a.ValidateLabelModelOverrides(), "agent edits must revalidate existing mappings")
	a.AgentType = nil
	require.Error(t, a.ValidateLabelModelOverrides(), "mapping requires an explicit agent")
	a.AgentType = &agent
	handler := &AutomationHandler{logger: zerolog.Nop(), orgStore: &fakeOrgStore{org: models.Organization{Settings: json.RawMessage(`{}`)}}}
	require.Error(t, handler.validateAutomationLabelModels(context.Background(), a), "unavailable team credential must reject mapped model configuration")
}
