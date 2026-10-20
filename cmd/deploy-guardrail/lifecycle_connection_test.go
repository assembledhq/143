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

func TestLifecycleOperatorConnectionPoolWiring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, path, timeout string
	}{
		{name: "deploy guardrail", path: "main.go", timeout: "15 * time.Second"},
		{name: "worker deploy control", path: "../worker-deployctl/main.go", timeout: "20 * time.Second"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.path, nil, 0)
			require.NoError(t, err, "inspect the actual operator command source rather than an unused helper")
			var mainFunction *ast.FuncDecl
			for _, declaration := range file.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "main" {
					mainFunction = function
				}
			}
			require.NotNil(t, mainFunction, "operator command must retain its actual main entrypoint")
			var constructors, poolContexts, databaseURLs []string
			var poolOptions []map[string]string
			var contextArguments [][]string
			ast.Inspect(mainFunction.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := operatorASTExpression(t, fset, call.Fun)
				if name == "context.WithTimeout" {
					arguments := make([]string, len(call.Args))
					for i, argument := range call.Args {
						arguments[i] = operatorASTExpression(t, fset, argument)
					}
					contextArguments = append(contextArguments, arguments)
				}
				if name != "db.NewPool" && name != "db.NewPoolWithOptions" && name != "db.NewPoolConfig" {
					return true
				}
				constructors = append(constructors, name)
				require.Len(t, call.Args, 3, "the live operator pool constructor must receive context, explicit configured database and options")
				poolContexts = append(poolContexts, operatorASTExpression(t, fset, call.Args[0]))
				databaseURLs = append(databaseURLs, operatorASTExpression(t, fset, call.Args[1]))
				options, ok := call.Args[2].(*ast.CompositeLit)
				require.True(t, ok, "live operator pool options must be explicit at the real constructor")
				require.Equal(t, "db.PoolOptions", operatorASTExpression(t, fset, options.Type), "operator commands must retain the shared typed pool options")
				fields := map[string]string{}
				for _, element := range options.Elts {
					field, ok := element.(*ast.KeyValueExpr)
					require.True(t, ok, "operator pool options must use named fields")
					fields[operatorASTExpression(t, fset, field.Key)] = operatorASTExpression(t, fset, field.Value)
				}
				poolOptions = append(poolOptions, fields)
				return true
			})
			require.Equal(t, []string{"db.NewPoolWithOptions"}, constructors, "each operator main must create exactly one bounded shared pool")
			require.Equal(t, []string{"ctx"}, poolContexts, "the operator pool must continue using the command's existing deadline context")
			require.Equal(t, []string{"cfg.DatabaseURL"}, databaseURLs, "connection attribution must not change database selection")
			require.Equal(t, []map[string]string{{"MaxConns": "1", "ApplicationName": "db.LifecycleOperatorConnectionApplicationName(version.BuildSHA)"}}, poolOptions, "the actual operator pool must retain one connection and opt in exactly once using its binary build")
			require.Equal(t, [][]string{{"context.Background()", tt.timeout}}, contextArguments, "passive attribution must preserve the existing operator command budget without adding deadline owners")
		})
	}
}

func operatorASTExpression(t *testing.T, fset *token.FileSet, node ast.Node) string {
	t.Helper()
	var rendered bytes.Buffer
	require.NoError(t, format.Node(&rendered, fset, node), "render the actual command expression for an exact wiring comparison")
	return rendered.String()
}

func TestLifecycleOperatorDockerBuildWiring(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../Dockerfile")
	require.NoError(t, err, "inspect the production image recipe that builds operator binaries")
	const flags = `-ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}"`
	tests := []struct {
		name, target, source string
	}{
		{name: "deploy guardrail", target: "/bin/deploy-guardrail", source: "./cmd/deploy-guardrail"},
		{name: "worker deploy control", target: "/bin/worker-deployctl", source: "./cmd/worker-deployctl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var commands []string
			for line := range strings.SplitSeq(string(contents), "\n") {
				if strings.Contains(line, "go build ") && strings.Contains(line, "-o "+tt.target+" ") {
					command := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), `&& \`))
					commands = append(commands, command)
				}
			}
			require.Equal(t, []string{"CGO_ENABLED=0 go build " + flags + " -o " + tt.target + " " + tt.source}, commands, "each operator image binary must embed the same build SHA as the server without changing its output or source")
			require.Contains(t, string(contents), "CGO_ENABLED=0 go build "+flags+" -o /bin/server ./cmd/server", "operator metadata must use the existing server SHA linker contract")
		})
	}
}
