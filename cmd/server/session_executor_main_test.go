package main

import (
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/worker"
)

func TestWireSessionExecutorSlackStores(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	stores := &worker.Stores{}

	wireSessionExecutorSlackStores(stores, mock)

	require.NotNil(t, stores.SlackInstallations, "session executor stores should include Slack installations")
	require.NotNil(t, stores.SlackOrgSelections, "session executor stores should include Slack org selections")
	require.NotNil(t, stores.SlackBotSettings, "session executor stores should include Slack bot settings")
	require.NotNil(t, stores.SlackUserLinks, "session executor stores should include Slack user links")
	require.NotNil(t, stores.SlackChannels, "session executor stores should include Slack channel settings")
	require.NotNil(t, stores.SlackSessionLinks, "session executor stores should include Slack session links for lifecycle enqueue hooks")
	require.NotNil(t, stores.SlackInboundEvents, "session executor stores should include Slack inbound events")
	require.NotNil(t, stores.SlackOutbound, "session executor stores should include Slack outbound messages")
	require.NotNil(t, stores.HumanInputRequests, "session executor stores should include human-input requests for Slack delivery")
}
