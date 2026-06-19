package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

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
