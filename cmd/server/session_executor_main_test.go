package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildSessionExecutorStoresIncludesSlackCompletionDependencies(t *testing.T) {
	t.Parallel()

	stores := buildSessionExecutorStores(sessionExecutorStoreDeps{})

	require.NotNil(t, stores.Jobs, "session executor stores should include jobs so Slack follow-up jobs can be enqueued")
	require.NotNil(t, stores.SessionMessages, "session executor stores should include session messages for final response extraction")
	require.NotNil(t, stores.SlackSessionLinks, "session executor stores should include Slack session links for final response routing")
	require.NotNil(t, stores.SlackOutbound, "session executor stores should include Slack outbound rows for delivery observability")
	require.NotNil(t, stores.SlackChannels, "session executor stores should include Slack channel settings")
	require.NotNil(t, stores.SlackBotSettings, "session executor stores should include Slack bot settings")
	require.NotNil(t, stores.SlackUserLinks, "session executor stores should include Slack user links")
	require.NotNil(t, stores.SlackInstallations, "session executor stores should include Slack installations")
	require.NotNil(t, stores.SlackOrgSelections, "session executor stores should include Slack org selections")
	require.NotNil(t, stores.SlackInboundEvents, "session executor stores should include Slack inbound events")
	require.NotNil(t, stores.SessionAttributions, "session executor stores should include Slack session attributions")
	require.NotNil(t, stores.HumanInputRequests, "session executor stores should include human input requests for Slack notifications")
	require.NotNil(t, stores.Memberships, "session executor stores should include memberships for Slack authorization")
	require.NotNil(t, stores.GitHubInstallations, "session executor stores should include GitHub installations used by Slack context")
	require.NotNil(t, stores.SandboxHolders, "session executor stores should include sandbox holders for runtime coordination")
}
