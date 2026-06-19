package preview

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

type fakeSessionPrewarmLLM struct {
	response     string
	err          error
	systemPrompt string
	userPrompt   string
}

func (f *fakeSessionPrewarmLLM) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	f.systemPrompt = systemPrompt
	f.userPrompt = userPrompt
	return f.response, f.err
}

type blockingSessionPrewarmLLM struct{}

func (blockingSessionPrewarmLLM) Complete(ctx context.Context, _, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestSessionPrewarmClassifier_ClassifyBuildsBoundedProductFocusedPrompt(t *testing.T) {
	t.Parallel()

	llm := &fakeSessionPrewarmLLM{response: `{"decision":"cache","confidence":0.82,"reason":"ui_change","explanation":"Likely frontend product work."}`}
	classifier := NewSessionPrewarmClassifier(llm, time.Second)

	result := classifier.Classify(context.Background(), SessionPrewarmClassifierInput{
		RepositoryFullName: "acme/web",
		RepositoryLanguage: "TypeScript",
		SessionSource:      "manual",
		UserPrompt:         "Build a new dashboard preview card with a product walkthrough. https://example.test/" + strings.Repeat("x", 700),
		IssueLabels:        []string{"frontend", "customer-facing"},
		CapacitySummary:    "speculative slots available",
		Phase:              "session_start",
	})

	require.Equal(t, models.PreviewSpeculativeDecisionCache, result.Decision, "classifier should return the parsed cache decision")
	require.Equal(t, "ui_change", result.Reason, "classifier should preserve the reason code")
	require.Contains(t, llm.systemPrompt, "frontend", "system prompt should explicitly prioritize frontend work")
	require.Contains(t, llm.systemPrompt, "product", "system prompt should explicitly prioritize product-level work")
	require.Contains(t, llm.userPrompt, "acme/web", "user prompt should include repository identity")
	require.NotContains(t, llm.userPrompt, "https://example.test", "user prompt should strip URLs from untrusted text")
	require.LessOrEqual(t, len(extractPromptField(llm.userPrompt, "User prompt")), 520, "user prompt field should be bounded near 500 characters")
}

func TestSessionPrewarmClassifier_ClassifyFailsClosedOnInvalidOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		err      error
	}{
		{name: "invalid json", response: `warm it up`},
		{name: "unknown decision", response: `{"decision":"live","confidence":1,"reason":"ui_change","explanation":"bad"}`},
		{name: "llm error", err: errors.New("rate limited")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			classifier := NewSessionPrewarmClassifier(&fakeSessionPrewarmLLM{response: tt.response, err: tt.err}, time.Second)

			result := classifier.Classify(context.Background(), SessionPrewarmClassifierInput{RepositoryFullName: "acme/api"})

			require.Equal(t, models.PreviewSpeculativeDecisionNone, result.Decision, "classifier failures should choose none")
			require.Equal(t, "classifier_error", result.Reason, "classifier failures should be recorded with an operator reason")
			require.Equal(t, "decided", result.Status, "non-timeout failures should be terminal decisions")
		})
	}
}

func TestSessionPrewarmClassifier_ClassifyTimesOut(t *testing.T) {
	t.Parallel()

	classifier := NewSessionPrewarmClassifier(blockingSessionPrewarmLLM{}, time.Nanosecond)

	result := classifier.Classify(context.Background(), SessionPrewarmClassifierInput{RepositoryFullName: "acme/web"})

	require.Equal(t, models.PreviewSpeculativeDecisionNone, result.Decision, "classifier timeout should choose none")
	require.Equal(t, "classifier_timeout", result.Status, "classifier timeout should record timeout status")
	require.Equal(t, "classifier_timeout", result.Reason, "classifier timeout should preserve reason")
}

func extractPromptField(prompt, label string) string {
	prefix := label + ": "
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}
