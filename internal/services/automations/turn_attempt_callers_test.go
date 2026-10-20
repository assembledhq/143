package automations

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is a bounded syntax guard, not semantic call resolution or authority
// enforcement. The actual callback surface must be reviewed if it grows: the
// facade deliberately inherits a silent store rather than implicitly buffering
// every current or future SessionStore method.
func TestTurnAttemptPersistenceActualCallbackShape(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("../agent")
	require.NoError(t, err, "inspect the actual orchestrator package without private notes or runtime configuration")
	methods := map[string]int{}
	callbacks, forwarded := 0, 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("../agent", entry.Name()), nil, 0)
		require.NoError(t, err, "parse the real non-test orchestrator caller source")
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "EndAttempt" && selector.Sel.Name != "endAutomationAttempt") {
				return true
			}
			require.NotEmpty(t, call.Args, "every attempt-owner call must retain its reviewed callback")
			callback, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
			require.True(t, ok, "new indirect attempt callbacks require explicit review rather than bypassing publisher classification")
			callbacks++
			boundNames := map[string]bool{}
			for _, parameter := range callback.Type.Params.List {
				if name, ok := parameter.Type.(*ast.Ident); ok && name.Name == "SessionStore" {
					for _, identifier := range parameter.Names {
						boundNames[identifier.Name] = true
					}
				}
			}
			require.Len(t, boundNames, 1, "each real owner callback must expose exactly its reviewed transaction-bound session parameter")
			var ancestors []ast.Node
			ast.Inspect(callback.Body, func(child ast.Node) bool {
				if child == nil {
					ancestors = ancestors[:len(ancestors)-1]
					return true
				}
				if identifier, ok := child.(*ast.Ident); ok && boundNames[identifier.Name] {
					parent := ancestors[len(ancestors)-1]
					if selector, ok := parent.(*ast.SelectorExpr); ok && selector.X == identifier {
						invocation, ok := ancestors[len(ancestors)-2].(*ast.CallExpr)
						require.True(t, ok && invocation.Fun == selector, "method values or aliased publisher calls require explicit callback-surface review")
					} else {
						invocation, ok := parent.(*ast.CallExpr)
						require.True(t, ok, "the callback store must not escape through a new alias or unreviewed helper")
						callee, ok := invocation.Fun.(*ast.Ident)
						require.True(t, ok && callee.Name == "write" && entry.Name() == "automation_turn.go", "only the reviewed endAutomationAttempt write callback may forward its bound store")
						forwarded++
					}
				}
				ancestors = append(ancestors, child)
				invocation, ok := child.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := invocation.Fun.(*ast.SelectorExpr)
				if !ok {
					callee, ok := invocation.Fun.(*ast.Ident)
					require.True(t, ok && callee.Name == "write", "new callback helpers require explicit transaction/publication classification")
					return true
				}
				receiver, ok := method.X.(*ast.Ident)
				if ok && boundNames[receiver.Name] {
					methods[method.Sel.Name]++
				} else {
					require.Contains(t, []string{"WriteResult", "RecordTurnDuration", "LockAttempt", "Errorf"}, method.Sel.Name, "callbacks must not bypass the facade through an original publisher or unreviewed helper")
				}
				return true
			})
			return false
		})
	}
	require.Equal(t, 7, callbacks, "new attempt callbacks require review of their transaction and publication behavior")
	require.Equal(t, 1, forwarded, "the sole reviewed callback indirection must remain explicitly visible to publisher classification")
	require.Equal(t, map[string]int{"UpdateTurnComplete": 3, "UpdateResult": 1, "UpdateStatus": 1, "UpdateSnapshotInfo": 1, "ClearContainerID": 1}, methods, "only reviewed deferred status/result/turn and row-only snapshot/container methods may be used by the real callbacks")

	main, err := parser.ParseFile(token.NewFileSet(), "../../../cmd/server/main.go", nil, 0)
	require.NoError(t, err, "inspect the actual production owner wiring")
	constructors, poolSessions := 0, 0
	ast.Inspect(main, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "NewTurnStore" {
			constructors++
			require.GreaterOrEqual(t, len(call.Args), 2, "the production owner must receive both its pool and original publisher store")
			pool, firstOK := call.Args[0].(*ast.Ident)
			sessions, secondOK := call.Args[1].(*ast.Ident)
			require.True(t, firstOK && secondOK && pool.Name == "pool" && sessions.Name == "sessionStore", "EndAttempt's physical-commit claim is scoped to its pool-backed production configuration, not arbitrary nested TurnStore construction")
		}
		if selector.Sel.Name == "NewSessionStore" && len(call.Args) == 1 {
			if pool, ok := call.Args[0].(*ast.Ident); ok && pool.Name == "pool" {
				poolSessions++
			}
		}
		return true
	})
	require.Equal(t, 1, constructors, "the production turn owner constructor must remain explicitly covered")
	require.Positive(t, poolSessions, "the production original session store must be constructed over the pool rather than an outer transaction")
}
