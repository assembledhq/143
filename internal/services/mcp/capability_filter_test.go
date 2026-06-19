package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

type staticToolSource struct {
	tools []Tool
}

func (s staticToolSource) ListTools() []Tool {
	return s.tools
}

func (s staticToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	return TextResult("called " + name)
}

func TestCapabilityFilteredToolSourceHidesDisallowedTools(t *testing.T) {
	t.Parallel()

	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "github_list_recent_prs"},
		{Name: "log_query"},
		{Name: "pr_create"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityPRHistory, AccessLevel: models.AgentCapabilityAccessRead},
	}})

	tools := source.ListTools()
	require.Equal(t, []Tool{{Name: "github_list_recent_prs"}}, tools, "only PR-history tools should remain visible")
}

func TestCapabilityFilteredToolSourceBlocksDirectCall(t *testing.T) {
	t.Parallel()

	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{{Name: "log_query"}}}, ToolCapabilityPolicy{})

	result := source.CallTool(context.Background(), "log_query", json.RawMessage(`{}`))
	require.True(t, result.IsError, "blocked tool calls should return an error result")
	require.Contains(t, result.Content[0].Text, "CAPABILITY_DENIED", "blocked tool call should explain capability denial")
}

func TestCapabilityFilteredToolSourceAllowsAutomationGoalImprovementComplete(t *testing.T) {
	t.Parallel()

	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "automation_goal_improvement_complete"},
		{Name: "pr_create"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityRepoContext, AccessLevel: models.AgentCapabilityAccessRead},
	}})

	require.Equal(t, []Tool{{Name: "automation_goal_improvement_complete"}}, source.ListTools(), "goal improvement sessions should keep their scoped completion tool visible")
	result := source.CallTool(context.Background(), "automation_goal_improvement_complete", json.RawMessage(`{}`))
	require.False(t, result.IsError, "goal improvement completion should remain callable after capability filtering")
}
