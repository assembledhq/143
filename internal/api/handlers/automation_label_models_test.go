package handlers

import (
	"context"
	"encoding/json"
	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

func TestAutomationExplicitRoutesCreateAndPatchRejectUnavailableAgent(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize API fixture")
			defer mock.Close()
			org, id := uuid.New(), uuid.New()
			now := time.Now()
			h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
			h.SetOrgStore(&stubAutomationOrgLookup{org: models.Organization{Settings: json.RawMessage(`{"agent_config":{"codex":{"OPENAI_API_KEY":"synthetic"}}}`)}})
			h.SetCodingCredentialStore(&stubAutomationCodingCredentialLookup{})
			filters := json.RawMessage(`{"check_run":{"name":"policy","app_id":1},"labels":["one","two"],"label_model_routes":{"one":{"agent_type":"codex","model":"gpt-6-astra"},"two":{"agent_type":"claude_code","model":"claude-fable-5"}}}`)
			body := map[string]any{"github_event_filters": filters}
			if method == http.MethodPost {
				body["name"] = "example"
				body["goal"] = "review"
				body["interval_value"] = 2
				body["interval_unit"] = "days"
			} else {
				mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").WithArgs(testAnyArgs(2)...).WillReturnRows(newAutomationRow(mock, models.Automation{ID: id, OrgID: org, Name: "example", Goal: "review", ExecutionMode: "sequential", BaseBranch: "main", GitHubEventTriggers: []models.AutomationGitHubEvent{models.AutomationGitHubEventCheckRunCompleted}, ScheduleType: models.AutomationScheduleNone, Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}))
			}
			req := newAutomationRequest(t, method, "/api/v1/automations/"+id.String(), body, org, uuid.New(), map[string]string{"id": id.String()})
			rr := httptest.NewRecorder()
			if method == http.MethodPost {
				h.Create(rr, req)
			} else {
				h.Update(rr, req)
			}
			require.Equal(t, http.StatusBadRequest, rr.Code, "all routed providers must have credentials before persistence")
			var result models.ErrorResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result), "decode API error")
			require.Equal(t, "INVALID_LABEL_MODELS", result.Error.Code, "create and patch must use route validation")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid routes must not persist")
		})
	}
}
