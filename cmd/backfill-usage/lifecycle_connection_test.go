package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
)

const lifecycleAuxiliaryProbeBuild = "0123456789aBcDeF0123456789aBcDeF01234567"

func TestBackfillUsagePoolFailure(t *testing.T) {
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
			pool, err := newBackfillUsagePool(ctx, tt.raw, "dev")
			require.Nil(t, pool, "an unsuccessful production factory must not return an owned pool")
			require.Error(t, err, "attribution must preserve the native parser or connection failure")
			if tt.cancelled {
				require.ErrorIs(t, err, context.Canceled, "a cancelled backfill must retain its cancellation cause")
			}
		})
	}
}

func TestBackfillUsageMainAttributionWiring(t *testing.T) {
	t.Parallel()
	fileSet := token.NewFileSet()
	source, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	require.NoError(t, err, "parse the real single-file backfill entrypoint")
	var calls []string
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "main" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if ok && name.Name == "newBackfillUsagePool" {
				var rendered bytes.Buffer
				require.NoError(t, format.Node(&rendered, fileSet, call), "render the actual factory call")
				calls = append(calls, rendered.String())
			}
			return true
		})
	}
	require.Equal(t, []string{"newBackfillUsagePool(ctx, dbURL, version.BuildSHA)"}, calls, "the real main must pass its unchanged URL, context and binary metadata to exactly one pool factory")
}

// A startup-only peer rejects authentication before any SQL can run. This is
// deliberately not a fake database: real PostgreSQL proofs live separately.
func TestAuxiliaryCommandBinaryAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, command, build, expected string
		singleFile                     bool
	}{
		{name: "backfill linked SHA", command: ".", build: lifecycleAuxiliaryProbeBuild, expected: "143:operator:" + lifecycleAuxiliaryProbeBuild},
		{name: "backfill single-file development entrypoint", command: "main.go", expected: "143:operator:dev", singleFile: true},
		{name: "waiter linked SHA", command: "../wait-postgres", build: lifecycleAuxiliaryProbeBuild, expected: "143:operator:" + lifecycleAuxiliaryProbeBuild},
		{name: "waiter development entrypoint", command: "../wait-postgres", expected: "143:operator:dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			binary := filepath.Join(t.TempDir(), "auxiliary")
			arguments := []string{"build", "-trimpath"}
			if tt.build != "" {
				arguments = append(arguments, "-ldflags", "-X github.com/assembledhq/143/internal/version.BuildSHA="+tt.build)
			}
			arguments = append(arguments, "-o", binary, tt.command)
			build := exec.CommandContext(ctx, "go", arguments...)
			if err := build.Run(); err != nil {
				t.Fatal("build the actual command without publishing compiler output or machine-local paths")
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err, "listen only on loopback for the test-owned startup capture")
			captured := make(chan auxiliaryStartupCapture, 1)
			go captureAuxiliaryStartup(listener, captured)
			defer func() {
				require.NoError(t, listener.Close(), "close the startup-only listener and unblock any remaining accept")
				select {
				case <-captured:
				case <-time.After(5 * time.Second):
					t.Error("join the test-owned startup listener before the test returns")
				}
			}()
			query := url.Values{
				"sslmode":          {"disable"},
				"application_name": {"synthetic-uri-name"},
				"APPLICATION_NAME": {"synthetic-uppercase-name"},
				"Application_Name": {"synthetic-mixed-name"},
			}
			raw := "postgres://synthetic@" + listener.Addr().String() + "/synthetic_target?" + query.Encode()
			child := exec.CommandContext(ctx, binary)
			if tt.singleFile {
				// Keep the documented go-run form executable, not merely
				// asserted as a comment or approximated by a package build.
				child = exec.CommandContext(ctx, "go", "run", "main.go", "--days", "0")
			}
			child.Env = auxiliaryCommandEnvironment("DATABASE_URL="+raw, "WAIT_POSTGRES_TIMEOUT=50ms", "PGAPPNAME=synthetic-environment-name")
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			commandErr := child.Run()
			require.Error(t, commandErr, "the startup-only peer must reject the real command before it can perform database work")
			var exit *exec.ExitError
			require.True(t, errors.As(commandErr, &exit) && exit.ExitCode() == 1, "the actual command must retain its existing startup-failure exit code")
			select {
			case capture := <-captured:
				// Return the item for the deferred join without racing buffers.
				captured <- capture
				require.True(t, capture.err == nil, "capture a native PostgreSQL startup frame before authentication rejection")
				require.Equal(t, tt.expected, capture.parameters["application_name"], "the actual command must send its linked SHA or dev default rather than a DSN or environment override")
				require.Equal(t, "synthetic_target", capture.parameters["database"], "command attribution must preserve the selected startup database")
				require.Equal(t, "synthetic", capture.parameters["user"], "command attribution must preserve the selected startup user")
				for key := range capture.parameters {
					if strings.EqualFold(key, "application_name") {
						require.Equal(t, "application_name", key, "only the canonical application-name key may reach the wire")
					}
					require.NotEqual(t, "lifecycle143.writer_version", key, "the real command must not inject writer-version startup authority")
				}
			case <-ctx.Done():
				t.Fatal("the compiled command must reach the startup peer within its test-owned deadline")
			}
			require.Zero(t, stdout.Len(), "startup failure must not add a success message or ordinary stdout output")
			require.Positive(t, stderr.Len(), "the existing command must still report a startup failure without publishing its captured diagnostics")
		})
	}
}

type auxiliaryStartupCapture struct {
	parameters map[string]string
	err        error
}

func captureAuxiliaryStartup(listener net.Listener, result chan<- auxiliaryStartupCapture) {
	connection, err := listener.Accept()
	if err != nil {
		result <- auxiliaryStartupCapture{err: err}
		return
	}
	capture := auxiliaryStartupCapture{}
	defer func() {
		if err := connection.Close(); err != nil && capture.err == nil {
			capture.err = err
		}
		result <- capture
	}()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		capture.err = err
		return
	}
	backend := pgproto3.NewBackend(connection, connection)
	message, err := backend.ReceiveStartupMessage()
	if err != nil {
		capture.err = err
		return
	}
	startup, ok := message.(*pgproto3.StartupMessage)
	if !ok {
		capture.err = errors.New("expected a native non-TLS startup frame")
		return
	}
	backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28000", Message: "synthetic startup rejection"})
	capture.parameters, capture.err = startup.Parameters, backend.Flush()
}

func auxiliaryCommandEnvironment(values ...string) []string {
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") || strings.HasPrefix(key, "LIFECYCLE_AUXILIARY_") || key == "DATABASE_URL" || key == "WAIT_POSTGRES_TIMEOUT" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, values...)
}

func auxiliaryKeywordValue(value string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
}

func auxiliaryProbeConnectionString(t *testing.T, raw, kind string) (string, []string) {
	t.Helper()
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse only the generated fixture URL without publishing the connection string")
	query := target.Query()
	query.Set("application_name", "synthetic-uri-name")
	query.Set("APPLICATION_NAME", "synthetic-uppercase-name")
	query.Set("Application_Name", "synthetic-mixed-name")
	query.Set("statement_timeout", "20000")
	query.Set("lock_timeout", "10000")
	query.Set("search_path", "public")
	query.Set("options", "-c application_name=synthetic-options-name -c idle_in_transaction_session_timeout=25000")
	query.Set("pool_min_conns", "0")
	query.Set("pool_min_idle_conns", "0")
	query.Set("pool_max_conns", "3")
	query.Set("pool_max_conn_idle_time", "23m")
	target.RawQuery = query.Encode()
	switch kind {
	case "uri":
		return target.String(), nil
	case "query database":
		query.Set("dbname", strings.TrimPrefix(target.Path, "/"))
		target.Path, target.RawPath, target.RawQuery = "/synthetic_unused", "", query.Encode()
		return target.String(), nil
	case "keyword", "environment":
		password, _ := target.User.Password()
		port := target.Port()
		if port == "" {
			port = strconv.Itoa(5432)
		}
		settings := []string{"application_name=synthetic-keyword-name", "APPLICATION_NAME=synthetic-uppercase-name", "Application_Name=synthetic-mixed-name", "statement_timeout=20000", "lock_timeout=10000", "search_path=public", "options='-c application_name=synthetic-options-name -c idle_in_transaction_session_timeout=25000'", "pool_min_conns=0", "pool_min_idle_conns=0", "pool_max_conns=3", "pool_max_conn_idle_time=23m"}
		if kind == "environment" {
			settings = []string{"statement_timeout=20000", "lock_timeout=10000", "search_path=public", "pool_min_conns=0", "pool_min_idle_conns=0", "pool_max_conns=3", "pool_max_conn_idle_time=23m"}
			return strings.Join(settings, " "), []string{"PGHOST=" + target.Hostname(), "PGPORT=" + port, "PGUSER=" + target.User.Username(), "PGPASSWORD=" + password, "PGDATABASE=" + strings.TrimPrefix(target.Path, "/"), "PGSSLMODE=" + query.Get("sslmode")}
		}
		settings = append(settings, "host="+auxiliaryKeywordValue(target.Hostname()), "port="+port, "user="+auxiliaryKeywordValue(target.User.Username()), "password="+auxiliaryKeywordValue(password), "dbname="+auxiliaryKeywordValue(strings.TrimPrefix(target.Path, "/")), "sslmode="+auxiliaryKeywordValue(query.Get("sslmode")))
		return strings.Join(settings, " "), nil
	default:
		t.Fatal("select a declared native pgx connection-string test case")
		return "", nil
	}
}
