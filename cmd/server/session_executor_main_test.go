package main

import (
	"go/ast"
	"go/parser"
	"go/token"
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

func TestSessionExecutorStoresWireSlackLifecycleDependencies(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "session_executor_main.go", nil, 0)
	require.NoError(t, err, "session_executor_main.go should parse")

	var storesLiteral *ast.CompositeLit
	ast.Inspect(file, func(node ast.Node) bool {
		if storesLiteral != nil {
			return false
		}
		lit, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		typ, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || typ.Sel.Name != "Stores" {
			return true
		}
		pkg, ok := typ.X.(*ast.Ident)
		if !ok || pkg.Name != "worker" {
			return true
		}
		storesLiteral = lit
		return false
	})
	require.NotNil(t, storesLiteral, "session executor should construct worker.Stores explicitly")

	fields := map[string]bool{}
	for _, element := range storesLiteral.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fields[key.Name] = true
	}

	require.True(t, fields["SlackInstallations"], "session executor stores should include Slack installations")
	require.True(t, fields["SlackOrgSelections"], "session executor stores should include Slack org selections")
	require.True(t, fields["SlackBotSettings"], "session executor stores should include Slack bot settings")
	require.True(t, fields["SlackUserLinks"], "session executor stores should include Slack user links")
	require.True(t, fields["SlackChannels"], "session executor stores should include Slack channel settings")
	require.True(t, fields["SlackSessionLinks"], "session executor stores should include Slack session links for lifecycle enqueue hooks")
	require.True(t, fields["SlackInboundEvents"], "session executor stores should include Slack inbound events")
	require.True(t, fields["SlackOutbound"], "session executor stores should include Slack outbound messages")
	require.True(t, fields["HumanInputRequests"], "session executor stores should include human-input requests for Slack delivery")
}
