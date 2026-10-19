package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
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

func TestWriterObserverPartialCoverageAndMaintenancePool(t *testing.T) {
	t.Parallel()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err, "parse normal server bootstrap")
	var wiredPool string
	ast.Inspect(file, func(node ast.Node) bool {
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
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		require.NoError(t, err, "inspect normal server imports")
		require.NotEqual(t, "github.com/assembledhq/143/internal/services/lifecyclewriter", path, "normal API and worker process observers must remain deferred in this partial inventory slice")
	}
}
