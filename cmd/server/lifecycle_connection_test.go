package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleConnectionServerModes(t *testing.T) {
	t.Parallel()

	const build = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		mode     string
		expected string
	}{
		{mode: "api", expected: "143:api:" + build},
		{mode: "worker", expected: "143:worker:" + build},
		{mode: "all", expected: "143:all:" + build},
		{mode: "session_executor"},
		{mode: "operator"},
		{mode: "API"},
		{mode: "unsupported"},
		{mode: ""},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, serverLifecycleConnectionApplicationName(tt.mode, build), "only supported normal server modes may override connection attribution")
		})
	}
}

func TestLifecycleConnectionPoolWiring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path       string
		call       string
		expression string
	}{
		{path: "main.go", call: "db.NewPoolWithOptions", expression: "serverLifecycleConnectionApplicationName(cfg.Mode, version.BuildSHA)"},
		{path: "worker_control_plane.go", call: "db.NewPoolConfig", expression: "serverLifecycleConnectionApplicationName(cfg.Mode, version.BuildSHA)"},
		{path: "session_executor_main.go", call: "db.NewPoolWithOptions", expression: "db.LifecycleConnectionApplicationName(models.LifecycleWriterRoleSessionExecutor, version.BuildSHA)"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.path, nil, 0)
			require.NoError(t, err, "inspect actual production pool wiring, not a comment or unused helper")
			var expressions []string
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name bytes.Buffer
				require.NoError(t, format.Node(&name, fset, call.Fun), "read the actual pool constructor")
				if name.String() != tt.call {
					return true
				}
				for _, argument := range call.Args {
					options, ok := argument.(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, element := range options.Elts {
						field, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := field.Key.(*ast.Ident)
						if !ok || key.Name != "ApplicationName" {
							continue
						}
						var rendered bytes.Buffer
						require.NoError(t, format.Node(&rendered, fset, field.Value), "read process role and binary build from live pool options")
						expressions = append(expressions, rendered.String())
					}
				}
				return true
			})
			require.Equal(t, []string{tt.expression}, expressions, "each runtime pool must opt in exactly once with its actual process role and binary build")
		})
	}
}
