package adapters

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

func TestBuildSystemPrompt_IncludesPMContext(t *testing.T) {
	t.Parallel()

	issue := &models.Issue{
		Title: "Test issue",
	}
	input := &agent.AgentInput{
		Issue: issue,
		PMContext: &agent.PMTaskContext{
			Approach:      "Check handlers/billing.go:42",
			Risk:          "Be careful with retries",
			Reasoning:     "High impact",
			RelatedIssues: []string{"Payment timeout"},
			RootCause:     "Missing nil check",
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Product Manager Analysis", "system prompt should include PM context header")
	require.Contains(t, prompt, "High impact", "system prompt should include PM reasoning")
	require.Contains(t, prompt, "Check handlers/billing.go:42", "system prompt should include PM approach")
	require.Contains(t, prompt, "Missing nil check", "system prompt should include PM root cause")
}
