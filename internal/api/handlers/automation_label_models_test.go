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

func TestAutomationExplicitRoutesCreateAndPatchExecutionSettings(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		efforts := []string{"", "high", "max"}
		if method == http.MethodPatch {
			efforts = append(efforts, "remove", "clear")
		}
		for _, effort := range efforts {
			t.Run(method+effort, func(t *testing.T) {
				t.Parallel()
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "initialize API fixture")
				defer mock.Close()
				org, id := uuid.New(), uuid.New()
				now := time.Now()
				h := NewAutomationHandler(db.NewAutomationStore(mock), db.NewAutomationRunStore(mock))
				settings := `{"default_agent_type":"opencode","agent_config":{"codex":{"OPENAI_API_KEY":"synthetic"}}}`
				if effort != "" {
					settings = `{"default_agent_type":"opencode","agent_config":{"codex":{"OPENAI_API_KEY":"synthetic"},"claude_code":{"ANTHROPIC_API_KEY":"synthetic"}}}`
				}
				h.SetOrgStore(&stubAutomationOrgLookup{org: models.Organization{Settings: json.RawMessage(settings)}})
				h.SetCodingCredentialStore(&stubAutomationCodingCredentialLookup{})
				filters := json.RawMessage(`{"check_run":{"name":"policy","app_id":1},"labels":["one","two"],"label_model_routes":{"one":{"agent_type":"codex","model":"gpt-6-astra"},"two":{"agent_type":"claude_code","model":"claude-fable-5"}}}`)
				body := map[string]any{"github_event_filters": filters}
				if effort == "high" || effort == "max" {
					body["reasoning_effort"] = effort
					body["pre_pr_review_loops"] = 2
				}
				var oldFilters json.RawMessage
				var oldEffort *models.ReasoningEffort
				if effort == "remove" || effort == "clear" {
					oldFilters = filters
					high := models.ReasoningEffortHigh
					oldEffort = &high
					body["github_event_filters"] = json.RawMessage(`{}`)
				}
				if effort == "clear" {
					body["reasoning_effort"] = ""
				}
				if method == http.MethodPost {
					body["name"] = "example"
					body["goal"] = "review"
					body["interval_value"] = 2
					body["interval_unit"] = "days"
				} else {
					mock.ExpectQuery("SELECT .+ FROM automations WHERE id =").WithArgs(testAnyArgs(2)...).WillReturnRows(newAutomationRow(mock, models.Automation{ID: id, OrgID: org, Name: "example", Goal: "review", ExecutionMode: "sequential", BaseBranch: "main", AgentType: func() *string { v := "opencode"; return &v }(), GitHubEventFilters: oldFilters, ReasoningEffort: oldEffort, GitHubEventTriggers: []models.AutomationGitHubEvent{models.AutomationGitHubEventCheckRunCompleted}, ScheduleType: models.AutomationScheduleNone, Timezone: "UTC", Enabled: true, CreatedAt: now, UpdatedAt: now}))
				}
				if effort == "high" || effort == "clear" {
					if method == http.MethodPost {
						mock.ExpectQuery("INSERT INTO automations").WithArgs(testAnyArgs(30)...).WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(id, now, now))
					} else {
						mock.ExpectExec("UPDATE automations SET").WithArgs(testAnyArgs(32)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					}
				}
				req := newAutomationRequest(t, method, "/api/v1/automations/"+id.String(), body, org, uuid.New(), map[string]string{"id": id.String()})
				rr := httptest.NewRecorder()
				if method == http.MethodPost {
					h.Create(rr, req)
				} else {
					h.Update(rr, req)
				}
				if effort == "high" || effort == "clear" {
					expected := http.StatusOK
					if method == http.MethodPost {
						expected = http.StatusCreated
					}
					require.Equal(t, expected, rr.Code, "route agents must govern common settings: %s", rr.Body.String())
					var result models.SingleResponse[models.Automation]
					require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
					if effort == "clear" {
						require.Nil(t, result.Data.ReasoningEffort)
						require.Zero(t, result.Data.PrePRReviewLoops)
					} else {
						require.Equal(t, models.ReasoningEffortHigh, *result.Data.ReasoningEffort)
						require.Equal(t, 2, result.Data.PrePRReviewLoops)
					}
				} else {
					require.Equal(t, http.StatusBadRequest, rr.Code, "invalid common settings or unavailable credentials must not persist")
					var result models.ErrorResponse
					require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &result))
					expected := "INVALID_LABEL_MODELS"
					if effort == "max" || effort == "remove" {
						expected = "INVALID_REASONING_EFFORT"
					}
					require.Equal(t, expected, result.Error.Code)
				}
				require.NoError(t, mock.ExpectationsWereMet(), "invalid routes must not persist")
			})
		}
	}
}
