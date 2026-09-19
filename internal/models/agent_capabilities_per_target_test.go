package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestrictCapabilitySnapshotForPerTargetTurn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []AgentCapabilitySnapshotItem
		want []AgentCapabilitySnapshotItem
	}{
		{name: "empty stays empty", in: nil, want: []AgentCapabilitySnapshotItem{}},
		{
			name: "read capabilities are kept and capped at read",
			in: []AgentCapabilitySnapshotItem{
				{ID: AgentCapabilitySessionHistory, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityReviewFeedback, AccessLevel: AgentCapabilityAccessWrite},
				{ID: AgentCapabilityPRHistory, AccessLevel: AgentCapabilityAccessPublish},
				{ID: AgentCapabilityIssueSources, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityTeamDocs, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityProductionDiagnostics, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityCodeReviewPolicy, AccessLevel: AgentCapabilityAccessWrite},
			},
			want: []AgentCapabilitySnapshotItem{
				{ID: AgentCapabilitySessionHistory, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityReviewFeedback, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityPRHistory, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityIssueSources, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityTeamDocs, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityProductionDiagnostics, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityCodeReviewPolicy, AccessLevel: AgentCapabilityAccessRead},
			},
		},
		{
			name: "write and bypass capabilities are dropped whatever the org granted",
			in: []AgentCapabilitySnapshotItem{
				{ID: AgentCapabilityPublishing, AccessLevel: AgentCapabilityAccessPublish},
				{ID: AgentCapabilityAutomationManagement, AccessLevel: AgentCapabilityAccessWrite},
				{ID: AgentCapabilitySlackNotifications, AccessLevel: AgentCapabilityAccessWrite},
				{ID: AgentCapabilityExternalComments, AccessLevel: AgentCapabilityAccessWrite},
				{ID: AgentCapabilityEvalAuthoring, AccessLevel: AgentCapabilityAccessWrite},
				{ID: AgentCapabilityCIHistory, AccessLevel: AgentCapabilityAccessRead},
				{ID: AgentCapabilityRepoContext, AccessLevel: AgentCapabilityAccessRead},
			},
			want: []AgentCapabilitySnapshotItem{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, RestrictCapabilitySnapshotForPerTargetTurn(tt.in), "the positive allowlist decides, not the grant")
		})
	}
}

func TestToolAllowlistEnvRoundTrip(t *testing.T) {
	t.Parallel()
	require.Equal(t, PerTargetToolAllowlist, ToolAllowlistFromEnvValue(ToolAllowlistEnvValue()), "the environment form round-trips")
	require.Nil(t, ToolAllowlistFromEnvValue(""), "unset means no allowlist")
	require.Equal(t, []string{"a:b"}, ToolAllowlistFromEnvValue(" a:b , "), "whitespace and empty entries are dropped")
}

func TestPerTargetToolScopes(t *testing.T) {
	t.Parallel()
	scopes := PerTargetToolScopes()
	require.Equal(t, PerTargetToolAllowlistScope, scopes[0], "the marker comes first")
	require.Len(t, scopes, len(PerTargetToolAllowlist)+1, "one scope per allowlisted tool")
	require.Equal(t, PerTargetToolAllowlist, ToolAllowlistFromScopes(scopes), "the allowlist round-trips through the scopes")
	require.Nil(t, ToolAllowlistFromScopes([]string{"tool:pr:create", "preview:read"}), "tool scopes without the marker are not an allowlist")
	for _, denied := range []string{"pr:create", "slack:send", "automation:run", "eval:add", "automation-goal-improvement:complete", "code-review-history:update_policy", "linear:update_task", "pagerduty:add_note", "preview:ensure", "capability:request"} {
		require.False(t, HasToolScope(scopes, ToolScope(denied)), "%s is denied", denied)
	}
	for _, allowed := range []string{"session-history:search", "code-review-history:policy", "github:get_pr_reviews", "linear:get_task", "pagerduty:list_oncalls", "notion:get_document", "slack:get_thread", "logs:stats", "capability:list"} {
		require.True(t, HasToolScope(scopes, ToolScope(allowed)), "%s is allowed", allowed)
	}
}
