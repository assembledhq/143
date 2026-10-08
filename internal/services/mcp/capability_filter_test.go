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

func TestCapabilityFilteredToolSourceWithToolAllowlist(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{Name: "session_history_search"}, {Name: "code_review_history_policy"}, {Name: "code_review_history_update_policy"},
		{Name: "github_list_recent_prs"}, {Name: "sentry_list_errors"}, {Name: "linear_get_task"}, {Name: "linear_update_task"},
		{Name: "slack_get_thread"}, {Name: "slack_send"}, {Name: "log_query"}, {Name: "pr_create"},
		{Name: "capability_list"}, {Name: "capability_request"}, {Name: "automation_goal_improvement_complete"}, {Name: "preview_ensure"},
	}
	source := NewCapabilityFilteredToolSource(staticToolSource{tools: tools}, ToolCapabilityPolicy{
		Capabilities: []models.AgentCapabilitySnapshotItem{
			{ID: models.AgentCapabilitySessionHistory, AccessLevel: models.AgentCapabilityAccessRead},
			{ID: models.AgentCapabilityReviewFeedback, AccessLevel: models.AgentCapabilityAccessRead},
			{ID: models.AgentCapabilityCodeReviewPolicy, AccessLevel: models.AgentCapabilityAccessWrite},
			{ID: models.AgentCapabilityIssueSources, AccessLevel: models.AgentCapabilityAccessRead},
			{ID: models.AgentCapabilityTeamDocs, AccessLevel: models.AgentCapabilityAccessRead},
			{ID: models.AgentCapabilityProductionDiagnostics, AccessLevel: models.AgentCapabilityAccessRead},
			{ID: models.AgentCapabilityPublishing, AccessLevel: models.AgentCapabilityAccessPublish},
			{ID: models.AgentCapabilitySlackNotifications, AccessLevel: models.AgentCapabilityAccessWrite},
			{ID: models.AgentCapabilityExternalComments, AccessLevel: models.AgentCapabilityAccessWrite},
		},
		ToolAllowlist: models.PerTargetToolAllowlist,
	})
	var visible []string
	for _, tool := range source.ListTools() {
		visible = append(visible, tool.Name)
	}
	require.Equal(t, []string{"session_history_search", "code_review_history_policy", "linear_get_task", "slack_get_thread", "log_query", "capability_list"}, visible,
		"only allowlisted tools with a grant remain; sentry, writes, publishing, and every bypass namespace are gone even when granted")
	require.True(t, source.CallTool(context.Background(), "preview_ensure", nil).IsError, "preview no longer bypasses the filter")
	require.True(t, source.CallTool(context.Background(), "automation_goal_improvement_complete", nil).IsError, "goal-improvement completion no longer bypasses the filter")
	require.True(t, source.CallTool(context.Background(), "capability_request", nil).IsError, "capability requests are denied")
	require.False(t, source.CallTool(context.Background(), "capability_list", nil).IsError, "capability self-inspection stays available")
	require.True(t, source.CallTool(context.Background(), "github_list_recent_prs", nil).IsError, "an allowlisted tool without its capability grant is still denied")
}

func TestCapabilityFilteredToolSourceEmptySnapshotWithAllowlistBlocksProviders(t *testing.T) {
	t.Parallel()
	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{{Name: "linear_get_task"}, {Name: "capability_list"}}}, ToolCapabilityPolicy{ToolAllowlist: models.PerTargetToolAllowlist})
	var visible []string
	for _, tool := range source.ListTools() {
		visible = append(visible, tool.Name)
	}
	require.Equal(t, []string{"capability_list"}, visible, "an empty snapshot under an allowlist exposes nothing that needs a grant")
}

func TestCapabilityFilteredToolSourcePolicyReadUnderAllowlist(t *testing.T) {
	t.Parallel()
	source := NewCapabilityFilteredToolSource(staticToolSource{tools: []Tool{{Name: "code_review_history_policy"}, {Name: "code_review_history_update_policy"}}}, ToolCapabilityPolicy{
		Capabilities:  []models.AgentCapabilitySnapshotItem{{ID: models.AgentCapabilityCodeReviewPolicy, AccessLevel: models.AgentCapabilityAccessRead}},
		ToolAllowlist: models.PerTargetToolAllowlist,
	})
	var visible []string
	for _, tool := range source.ListTools() {
		visible = append(visible, tool.Name)
	}
	require.Equal(t, []string{"code_review_history_policy"}, visible, "a policy-management grant keeps the policy read and loses the update under the allowlist")
}
