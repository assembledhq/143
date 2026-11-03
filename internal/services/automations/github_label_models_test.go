package automations

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestGitHubLabelModelUsesFreshLabelsAndSingleHeadClaim(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, raw, firstModel, secondModel string
		secondAgent                        models.AgentType
	}{
		{"legacy", `{"check_run":{"name":"review-policy","app_id":123},"labels":["review-basic","review-deep"],"label_model_overrides":{"review-basic":"gpt-5.6-sol","review-deep":"gpt-5.6-terra"}}`, "gpt-5.6-sol", "gpt-5.6-terra", models.AgentTypeCodex},
		{"cross provider", `{"check_run":{"name":"review-policy","app_id":123},"labels":["review-basic","review-deep"],"label_model_routes":{"review-basic":{"agent_type":"codex","model":"gpt-6-astra"},"review-deep":{"agent_type":"claude_code","model":"claude-fable-5"}}}`, models.CodexModelGPT6Astra, models.ClaudeCodeModelFable5, models.AgentTypeClaudeCode},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, runs, jobs, head, req := checkRunFixture(t)
			store := s.automations.(*fakeGitHubAutomationStore)
			agent := string(models.AgentTypeCodex)
			store.automations[0].AgentType = &agent
			store.automations[0].GitHubEventFilters = json.RawMessage(tt.raw)
			head.head.Labels = []string{"REVIEW-BASIC"}
			req.LabelsKnown, req.Labels = true, []string{"review-deep"}
			require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "fresh labels should override stale webhook labels")
			first := runs.runs[0]
			head.head.Labels = []string{"review-deep"}
			req.ProviderEventID = "republish"
			require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "changed tier on the same head should dedupe")
			require.Len(t, jobs.jobs, 1, "same head must not dispatch a second model")
			selected, err := models.AutomationWithRunLabelModel(store.automations[0], first)
			require.NoError(t, err, "first frozen selection should decode")
			require.Equal(t, tt.firstModel, *selected.ModelOverride, "first model must survive republish")
			req.HeadSHA, head.head.SHA = "head-two", "head-two"
			require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "new head should select the current model")
			selected, err = models.AutomationWithRunLabelModel(store.automations[0], runs.runs[1])
			require.NoError(t, err, "new frozen selection should decode")
			require.Equal(t, tt.secondModel, *selected.ModelOverride, "new head should use the mapped model")
			require.Equal(t, string(tt.secondAgent), *selected.AgentType, "new head should choose the routed agent")
			req.HeadSHA, head.head.SHA = "head-three", "head-three"
			head.head.Labels = []string{"review-basic", "review-deep"}
			require.ErrorIs(t, s.TriggerGitHubEvent(context.Background(), req), ErrCheckRunTrigger, "conflicting labels should remain retryable")
			require.Len(t, jobs.jobs, 2, "conflicting labels cannot dispatch")
			head.head.Labels = []string{"review-basic"}
			require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "corrected labels should retry without consumed claim")
			require.Len(t, jobs.jobs, 3, "corrected head should dispatch exactly once")
			if tt.name == "cross provider" {
				req.HeadSHA, head.head.SHA = "head-four", "head-four"
				var invalid models.AutomationGitHubEventFilters
				require.NoError(t, json.Unmarshal(store.automations[0].GitHubEventFilters, &invalid), "decode routes for invalid fixture")
				invalid.LabelModelRoutes["review-basic"] = models.AutomationLabelModelRoute{AgentType: models.AgentTypeClaudeCode, Model: models.CodexModelGPT6Astra}
				raw, err := json.Marshal(invalid)
				require.NoError(t, err, "encode invalid route")
				store.automations[0].GitHubEventFilters = raw
				require.ErrorIs(t, s.TriggerGitHubEvent(context.Background(), req), ErrCheckRunTrigger, "wrong provider must fail before claiming head")
				require.Len(t, jobs.jobs, 3, "invalid route cannot dispatch")
				store.automations[0].GitHubEventFilters = json.RawMessage(tt.raw)
				require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "repaired route should retry the unclaimed head")
				require.Len(t, jobs.jobs, 4, "repair should claim and dispatch once")
			}

		})
	}
}
