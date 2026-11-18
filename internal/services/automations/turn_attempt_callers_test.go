package automations

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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

// The silent facade inherits these two reviewed methods. Pin their bounded
// source shape too: a future notification/helper must not be silently dropped
// just because the actual callback still calls the same method name. This is
// deliberately not whole-program publisher or SQL analysis.
func TestTurnAttemptPersistenceInheritedMethodsRemainRowOnly(t *testing.T) {
	t.Parallel()
	actual, err := os.ReadFile("../../db/session_store.go")
	require.NoError(t, err, "inspect only the actual inherited session store methods")
	snapshotBody := "_, err := s.db.Exec(ctx, query, pgx.NamedArgs{}); "
	tests := []struct {
		name, method, body string
		allowed            bool
	}{
		{name: "actual snapshot method", method: "UpdateSnapshotInfo", allowed: true},
		{name: "actual container CAS", method: "ClearContainerID", allowed: true},
		{name: "reviewed row-only fixture", method: "UpdateSnapshotInfo", body: snapshotBody + "return err", allowed: true},
		{name: "new direct publisher requires review", method: "UpdateSnapshotInfo", body: snapshotBody + "s.PublishCommittedSessionStatus(ctx); return err"},
		{name: "new indirect helper requires review", method: "UpdateSnapshotInfo", body: snapshotBody + "publishWorkspace(s); return err"},
		{name: "new stream access requires review", method: "UpdateSnapshotInfo", body: snapshotBody + "_ = s.streams; return err"},
		{name: "aliased publisher requires review", method: "UpdateSnapshotInfo", body: snapshotBody + "publish := s.publishWorkspaceGenerationChanged; publish(ctx); return err"},
	}
	reviewed := map[string]turnAttemptRowOnlyMethodSyntax{
		"UpdateSnapshotInfo": {
			calls:     []string{"s.db.Exec"},
			selectors: []string{"s.db.Exec", "s.db", "pgx.NamedArgs"},
		},
		"ClearContainerID": {
			calls:     []string{"s.db.Exec", "fmt.Errorf", "tag.RowsAffected"},
			selectors: []string{"s.db.Exec", "s.db", "pgx.NamedArgs", "fmt.Errorf", "tag.RowsAffected"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := actual
			if tt.body != "" {
				source = []byte("package db\nfunc (s *SessionStore) " + tt.method + "(ctx context.Context) error { " + tt.body + " }")
			}
			files := token.NewFileSet()
			file, err := parser.ParseFile(files, "session_store.go", source, 0)
			require.NoError(t, err, "parse the actual method or synthetic publisher-regression fixture")
			var methods []*ast.FuncDecl
			for _, declaration := range file.Decls {
				method, ok := declaration.(*ast.FuncDecl)
				if ok && method.Name.Name == tt.method && method.Recv != nil {
					methods = append(methods, method)
				}
			}
			require.Len(t, methods, 1, "the exact inherited method must remain explicitly visible to the row-only guard")
			shape := turnAttemptInheritedMethodSyntax(t, files, methods[0])
			want := reviewed[tt.method]
			allowed := slices.Equal(want.calls, shape.calls) && slices.Equal(want.selectors, shape.selectors)
			require.Equal(t, tt.allowed, allowed, "any new direct/indirect publisher, stream reference or unreviewed helper in the inherited method requires explicit effect classification; observed calls=%v selectors=%v", shape.calls, shape.selectors)
		})
	}
}

type turnAttemptRowOnlyMethodSyntax struct{ calls, selectors []string }

func turnAttemptInheritedMethodSyntax(t *testing.T, files *token.FileSet, method *ast.FuncDecl) turnAttemptRowOnlyMethodSyntax {
	t.Helper()
	text := func(node ast.Node) string {
		var formatted bytes.Buffer
		require.NoError(t, format.Node(&formatted, files, node), "format only the reviewed method receiver, call or selector")
		return formatted.String()
	}
	require.Len(t, method.Recv.List, 1, "the row-only guard must inspect the concrete SessionStore receiver")
	require.Equal(t, "*SessionStore", text(method.Recv.List[0].Type), "a new receiver implementation needs explicit callback-surface review")
	var shape turnAttemptRowOnlyMethodSyntax
	ast.Inspect(method.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			shape.calls = append(shape.calls, text(value.Fun))
		case *ast.SelectorExpr:
			shape.selectors = append(shape.selectors, text(value))
		}
		return true
	})
	return shape
}
