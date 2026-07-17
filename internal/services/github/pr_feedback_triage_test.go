package github

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestTriageFeedbackWithLLM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  string
		llmErr    error
		expected  models.PRFeedbackTriageResult
		expectErr bool
	}{
		{name: "change request", response: `{"intent":"change_request","requires_agent":true,"requires_code_change":true,"reason":"requests a test"}`, expected: models.PRFeedbackTriageResult{Intent: models.PRFeedbackIntentChangeRequest, RequiresAgent: true, RequiresCodeChange: true, Reason: "requests a test"}},
		{name: "markdown fenced response", response: "```json\n{\"intent\":\"question\",\"requires_agent\":true,\"requires_code_change\":false,\"reason\":\"needs repository context\"}\n```", expected: models.PRFeedbackTriageResult{Intent: models.PRFeedbackIntentQuestion, RequiresAgent: true, Reason: "needs repository context"}},
		{name: "unknown intent rejected", response: `{"intent":"unknown","requires_agent":false,"requires_code_change":false,"reason":"unclear"}`, expectErr: true},
		{name: "invalid JSON rejected", response: `not json`, expectErr: true},
		{name: "provider error", llmErr: errors.New("provider unavailable"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := &mockLLMClient{response: tt.response, err: tt.llmErr}
			service := &PRService{llmClient: client}
			actual, err := service.triageFeedbackWithLLM(context.Background(), models.PullRequestFeedbackItem{AuthorLogin: "reviewer", AuthorType: models.PRFeedbackAuthorTypeUser, Surface: models.PRFeedbackSurfaceIssueComment, Body: "Please check this"})
			if tt.expectErr {
				require.Error(t, err, "invalid or failed triage should return an error")
				return
			}
			require.NoError(t, err, "valid triage should succeed")
			require.Equal(t, tt.expected, actual, "triage should return the exact validated contract")
			require.Contains(t, client.lastSystemPrompt, "Return one JSON object", "triage should use the centralized prompt template")
		})
	}
}
