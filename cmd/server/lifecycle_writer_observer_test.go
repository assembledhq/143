package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionExecutorWriterObservationLifetimeWiring(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("session_executor_main.go")
	require.NoError(t, err, "read standalone executor entrypoint wiring")
	body := string(source)
	anchors := []string{
		"defer pool.Close()",
		"stopWriterObserver := lifecyclewriter.StartSessionExecutor(db.NewLifecycleWriterInstanceStore(pool), version.BuildSHA, logger)",
		"defer stopWriterObserver()",
		"runtime, shutdown, err := buildSessionExecutorRuntime(ctx, cfg, pool, logger)",
		"defer shutdown()",
		"runtime.Run(ctx, executorID)",
	}
	previous := -1
	for _, anchor := range anchors {
		position := strings.Index(body, anchor)
		require.NotEqual(t, -1, position, "standalone bootstrap must retain the explicit lifecycle seam: %s", anchor)
		require.Greater(t, position, previous, "defer ordering must keep observations through runtime cleanup and join before pool closure: %s", anchor)
		previous = position
	}
	require.Equal(t, 1, strings.Count(body, "lifecyclewriter.StartSessionExecutor("), "one process must start only one passive observer")
}

func TestWriterObserverServerPoolAndLifetimeWiring(t *testing.T) {
	t.Parallel()
	source, err := os.ReadFile("main.go")
	require.NoError(t, err, "read normal server bootstrap")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", source, 0)
	require.NoError(t, err, "parse normal server bootstrap")
	var wiredPool string
	var observerCalls []string
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				if owner, ok := selector.X.(*ast.Ident); ok && owner.Name == "lifecyclewriter" && selector.Sel.Name == "StartServer" {
					var rendered bytes.Buffer
					require.NoError(t, format.Node(&rendered, fset, call), "inspect the actual server observer call rather than a comment")
					observerCalls = append(observerCalls, rendered.String())
				}
			}
		}
		kv, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "LifecycleWriters" {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		if id, ok := call.Args[0].(*ast.Ident); ok {
			wiredPool = id.Name
		}
		return true
	})
	require.Equal(t, "pool", wiredPool, "global maintenance must use the existing ordinary pool, not the two-connection worker reserve")
	require.Equal(t, []string{"lifecyclewriter.StartServer(db.NewLifecycleWriterInstanceStore(pool), cfg.Mode, version.BuildSHA, logger)"}, observerCalls, "one configured server process must observe the real binary on its ordinary pool exactly once")
	anchors := []string{
		"\n\t\tpool.Close()\n\t}()",
		"db.EnsureAnthropicSplitSentinel(ctx, pool)",
		"stopWriterObserver := lifecyclewriter.StartServer(",
		"defer stopWriterObserver()",
		"shutdownDone := make(chan struct{})",
		"defer close(shutdownDone)",
		"if err := srv.Shutdown(shutdownCtx)",
		"if sandboxAuthShutdown != nil",
		"serveAndWaitForShutdown(func() error { return srv.Serve(ln) }, shutdownDone)",
	}
	previous := -1
	for _, anchor := range anchors {
		position := strings.Index(string(source), anchor)
		require.NotEqual(t, -1, position, "normal server must retain the coordinated observer lifetime seam: %s", anchor)
		require.Greater(t, position, previous, "normal shutdown completion must precede observer stop and deferred pool closure: %s", anchor)
		previous = position
	}
}
