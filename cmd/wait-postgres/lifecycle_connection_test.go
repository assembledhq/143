package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLifecycleWaiterFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		cancelled bool
	}{
		{name: "native parser error", raw: "postgres://synthetic:%zz@localhost/unused"},
		{name: "cancelled connection", raw: "postgres://synthetic@127.0.0.1:1/unused?sslmode=disable", cancelled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			connection, err := connectLifecycleWaiter(ctx, tt.raw, "dev")
			require.Nil(t, connection, "unsuccessful attributed connections must not return an owned backend")
			require.Error(t, err, "passive attribution must preserve native parse or connection failures")
			if tt.cancelled {
				require.ErrorIs(t, err, context.Canceled, "direct connection cancellation must remain discoverable through wrapping")
			}
			waitErr := waitForPostgres(ctx, tt.raw, 50*time.Millisecond)
			require.Error(t, waitErr, "a failing first attempt must still respect the existing short outer retry budget")
			if tt.cancelled {
				require.ErrorIs(t, waitErr, context.Canceled, "the polling entrypoint must retain a cancelled attempt's cause")
			} else {
				require.Equal(t, err.Error(), waitErr.Error(), "parsing must occur inside the attempt and return the unchanged native error")
			}
		})
	}
}

func TestLifecycleWaiterCancellationDuringRetry(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := waitForPostgres(ctx, "postgres://synthetic:%zz@localhost/unused", time.Minute)
	require.ErrorIs(t, err, context.DeadlineExceeded, "cancellation while waiting to retry must take precedence over the earlier parse error")
}

func TestLifecycleWaiterAttemptWiring(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	source, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	require.NoError(t, err, "parse the actual waiter implementation")
	var calls []string
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "waitForPostgres" {
			continue
		}
		for _, statement := range function.Body.List {
			loop, ok := statement.(*ast.ForStmt)
			if !ok {
				continue
			}
			ast.Inspect(loop.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				identifier, ok := call.Fun.(*ast.Ident)
				if ok && identifier.Name == "connectLifecycleWaiter" {
					var rendered bytes.Buffer
					require.NoError(t, format.Node(&rendered, fileSet, call), "render the attributed connection call found inside the actual retry loop")
					calls = append(calls, rendered.String())
				}
				return true
			})
		}
	}
	require.Equal(t, []string{"connectLifecycleWaiter(attemptCtx, dbURL, version.BuildSHA)"}, calls, "every retry must parse and label the original DSN under its existing five-second attempt context and actual binary metadata")
}
