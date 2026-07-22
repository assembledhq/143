package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

type staticToolSource struct {
	tools  []Tool
	result *ToolCallResult
}

func (s staticToolSource) ListTools() []Tool {
	return s.tools
}

func (s staticToolSource) CallTool(ctx context.Context, name string, args json.RawMessage) *ToolCallResult {
	if s.result != nil {
		return s.result
	}
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

func TestCapabilityFilteredToolSourceAllowsCodeReviewHistoryWithReviewFeedbackGrant(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{Name: "code_review_history_list"},
		{Name: "code_review_history_get"},
		{Name: "code_review_history_policy"},
		{Name: "code_review_history_update_policy"},
		{Name: "log_query"},
	}

	granted := NewCapabilityFilteredToolSource(staticToolSource{tools: tools}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityReviewFeedback, AccessLevel: models.AgentCapabilityAccessRead},
	}})
	require.Equal(t, []Tool{
		{Name: "code_review_history_list"},
		{Name: "code_review_history_get"},
		{Name: "code_review_history_policy"},
	}, granted.ListTools(), "review feedback capability should expose the read-only code review history tools, never the policy write")

	denied := NewCapabilityFilteredToolSource(staticToolSource{tools: tools}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilitySessionHistory, AccessLevel: models.AgentCapabilityAccessRead},
	}})
	result := denied.CallTool(context.Background(), "code_review_history_list", json.RawMessage(`{}`))
	require.True(t, result.IsError, "code review history should stay blocked without the review feedback grant")
	require.Contains(t, result.Content[0].Text, "CAPABILITY_DENIED", "blocked call should explain capability denial")

	writeDenied := granted.CallTool(context.Background(), "code_review_history_update_policy", json.RawMessage(`{}`))
	require.True(t, writeDenied.IsError, "policy updates should stay blocked under a read-only grant")
	require.Contains(t, writeDenied.Content[0].Text, "CAPABILITY_DENIED", "blocked policy write should explain capability denial")

	writeGranted := NewCapabilityFilteredToolSource(staticToolSource{tools: tools}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityCodeReviewPolicy, AccessLevel: models.AgentCapabilityAccessWrite},
	}})
	require.Equal(t, []Tool{
		{Name: "code_review_history_policy"},
		{Name: "code_review_history_update_policy"},
	}, writeGranted.ListTools(), "the policy management capability should expose the policy write plus the policy read it depends on, never the review history reads")
}

func TestCapabilityFilteredToolSourceAllowsSessionPreviewTools(t *testing.T) {
	t.Parallel()
	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{{Name: "preview_ensure"}, {Name: "preview_observe"}, {Name: "preview_act"}}}, ToolCapabilityPolicy{})
	require.Equal(t, []Tool{{Name: "preview_ensure"}, {Name: "preview_observe"}, {Name: "preview_act"}}, source.ListTools(), "session preview tools should remain available under capability filtering")
}

func TestCapabilityFilteredToolSourceSeparatesPagerDutyReadsAndWrites(t *testing.T) {
	t.Parallel()

	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "pagerduty_list_incidents"},
		{Name: "pagerduty_list_notes"},
		{Name: "pagerduty_add_note"},
		{Name: "pagerduty_create_status_update"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityIssueSources, AccessLevel: models.AgentCapabilityAccessRead},
	}})

	require.Equal(t, []Tool{{Name: "pagerduty_list_incidents"}, {Name: "pagerduty_list_notes"}}, source.ListTools(), "issue-source capability should allow PagerDuty reads but not writebacks")
	writeResult := source.CallTool(context.Background(), "pagerduty_create_status_update", json.RawMessage(`{}`))
	require.True(t, writeResult.IsError, "PagerDuty writeback should require external-comments capability")

	writeSource := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "pagerduty_add_note"},
		{Name: "pagerduty_create_status_update"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityExternalComments, AccessLevel: models.AgentCapabilityAccessWrite},
	}})

	require.Equal(t, []Tool{{Name: "pagerduty_add_note"}, {Name: "pagerduty_create_status_update"}}, writeSource.ListTools(), "external-comments capability should allow PagerDuty writebacks")
}

func TestCapabilityFilteredToolSourceAllowsSlackSendOnlyWithSlackNotificationGrant(t *testing.T) {
	t.Parallel()

	readSource := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "slack_search_messages"},
		{Name: "slack_send"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityTeamDocs, AccessLevel: models.AgentCapabilityAccessRead},
	}})
	require.Equal(t, []Tool{{Name: "slack_search_messages"}}, readSource.ListTools(), "team docs should allow Slack reads but not message sending")

	writeSource := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "slack_send"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilitySlackNotifications, AccessLevel: models.AgentCapabilityAccessWrite},
	}})
	require.Equal(t, []Tool{{Name: "slack_send"}}, writeSource.ListTools(), "Slack notification capability should allow slack send")
}

func TestCapabilityFilteredToolSourceAllowsAutomationManagementTools(t *testing.T) {
	t.Parallel()

	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{
		{Name: "automation_create"},
		{Name: "automation_update"},
		{Name: "automation_run"},
		{Name: "automation_pause"},
		{Name: "automation_resume"},
		{Name: "slack_send"},
	}}, ToolCapabilityPolicy{Capabilities: []models.AgentCapabilitySnapshotItem{
		{ID: models.AgentCapabilityAutomationManagement, AccessLevel: models.AgentCapabilityAccessWrite},
	}})

	require.Equal(t, []Tool{
		{Name: "automation_create"},
		{Name: "automation_update"},
		{Name: "automation_run"},
		{Name: "automation_pause"},
		{Name: "automation_resume"},
	}, source.ListTools(), "automation management capability should allow automation tools only")
}
