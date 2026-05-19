package prompts

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReviewLoopReviewPrompt(t *testing.T) {
	t.Parallel()

	got := ReviewLoopReviewPrompt(ReviewLoopReviewPromptData{
		AgentType: models.AgentTypeClaudeCode,
	})
	require.Contains(t, got, "/review", "review prompt should include the native slash command")
	require.Contains(t, got, "Fix nits when they are local, low-risk", "review prompt should include the nit policy")
	require.Contains(t, got, "current workspace diff", "review prompt should target the current sandbox diff")
	require.NotContains(t, got, "files already touched", "review prompt should not block relevant fixes only because the file was untouched")
	require.Contains(t, got, "stale or outdated adjacent code", "review prompt should allow relevant low-risk stale-code cleanup")
}

func TestReviewLoopDecisionPrompt(t *testing.T) {
	t.Parallel()

	got := ReviewLoopDecisionPrompt()
	require.Contains(t, got, "REVIEW_CLEAN", "decision prompt should expose the clean sentinel")
	require.Contains(t, got, "NEEDS_FIX_PASS", "decision prompt should expose the fix sentinel")
	require.Contains(t, got, "Answer with one of", "decision prompt should constrain the response")
	require.Contains(t, got, "coding judgment", "decision prompt should ask the agent to apply coding judgment")
}

func TestReviewLoopFixPrompt(t *testing.T) {
	t.Parallel()

	got := ReviewLoopFixPrompt()
	require.Contains(t, got, "Fix the issues you identified", "fix prompt should refer to the agent's previous review")
	require.Contains(t, got, "Preserve the scope", "fix prompt should constrain broad rewrites")
	require.Contains(t, got, "Run relevant verification", "fix prompt should ask for verification")
}
