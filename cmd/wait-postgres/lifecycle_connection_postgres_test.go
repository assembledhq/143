package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestLifecycleWaiterConnectionPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("LIFECYCLE_WAITER_CHILD") == "1" {
		requireLifecycleWaiterConnectionState(t)
		return
	}
	raw, database := newLifecycleWaiterPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, waitForPostgres(ctx, raw, time.Second), "the real waiter must connect and successfully ping the full-schema fixture")
	inspection, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "open an independent observer without exposing its fixture connection string")
	var retained int
	require.NoError(t, inspection.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='143:operator:dev' AND pid<>pg_backend_pid()`).Scan(&retained), "inspect this unique database before any parallel factory probes start")
	require.Zero(t, retained, "successful waitForPostgres must close its own attributed backend before returning")
	require.NoError(t, inspection.Close(ctx), "close the independent observer before starting parallel child probes")
	const build = "0123456789aBcDeF0123456789aBcDeF01234567"
	tests := []struct {
		name, kind, build, expected string
	}{
		{name: "URI aliases and options", kind: "uri", build: build, expected: "143:operator:" + build},
		{name: "query database target", kind: "query database", build: build, expected: "143:operator:" + build},
		{name: "keyword aliases and options", kind: "keyword", build: "dev", expected: "143:operator:dev"},
		{name: "environment connection defaults", kind: "environment", build: build, expected: "143:operator:" + build},
		{name: "unsafe metadata fingerprint", kind: "uri", build: "build-a", expected: "143:operator:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			connectionString, defaults := lifecycleWaiterProbeConnectionString(t, raw, tt.kind)
			expectedIdle := "25s"
			if tt.kind == "environment" {
				expectedIdle = "26s"
			}
			values := append([]string{
				"LIFECYCLE_WAITER_CHILD=1", "LIFECYCLE_WAITER_URL=" + connectionString,
				"LIFECYCLE_WAITER_DATABASE=" + database, "LIFECYCLE_WAITER_BUILD=" + tt.build,
				"LIFECYCLE_WAITER_EXPECTED=" + tt.expected, "LIFECYCLE_WAITER_IDLE=" + expectedIdle,
				"PGAPPNAME=synthetic-environment-name", "PGOPTIONS=-c application_name=synthetic-environment-options -c idle_in_transaction_session_timeout=26000",
			}, defaults...)
			childCtx, childCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer childCancel()
			child := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestLifecycleWaiterConnectionPostgres$", "-test.timeout=15s")
			child.Env = lifecycleWaiterEnvironment(values...)
			var output bytes.Buffer
			child.Stdout, child.Stderr = &output, &output
			require.True(t, child.Run() == nil, "the real waiter connection factory must preserve native targets and unrelated settings while overriding every application-name source without writer authority")
		})
	}
}

func requireLifecycleWaiterConnectionState(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := connectLifecycleWaiter(ctx, os.Getenv("LIFECYCLE_WAITER_URL"), os.Getenv("LIFECYCLE_WAITER_BUILD"))
	require.True(t, err == nil, "open the actual waiter connection without publishing its connection error or DSN")
	defer func() {
		require.NoError(t, connection.Close(ctx), "close this case's independently owned real backend")
	}()
	require.NoError(t, connection.Ping(ctx), "attributed real waiter connections must support the unchanged readiness ping")
	type state struct {
		database, application, statementTimeout, lockTimeout, searchPath, idleTimeout string
		writerVersion                                                                 *string
		registryRows                                                                  int
	}
	actual := state{}
	err = connection.QueryRow(ctx, `SELECT current_database(),current_setting('application_name'),current_setting('statement_timeout'),current_setting('lock_timeout'),current_setting('search_path'),current_setting('idle_in_transaction_session_timeout'),current_setting('lifecycle143.writer_version',true),(SELECT count(*) FROM lifecycle_writer_instances)`).Scan(&actual.database, &actual.application, &actual.statementTimeout, &actual.lockTimeout, &actual.searchPath, &actual.idleTimeout, &actual.writerVersion, &actual.registryRows)
	require.NoError(t, err, "inspect actual PostgreSQL GUCs and the complete-schema writer registry")
	expected := state{database: os.Getenv("LIFECYCLE_WAITER_DATABASE"), application: os.Getenv("LIFECYCLE_WAITER_EXPECTED"), statementTimeout: "20s", lockTimeout: "10s", searchPath: "public", idleTimeout: os.Getenv("LIFECYCLE_WAITER_IDLE")}
	require.Equal(t, expected, actual, "only safe passive application naming may change; all targets, unrelated settings and empty authority state must remain exact")
	var observed string
	require.NoError(t, connection.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&observed), "inspect exactly this real waiter backend's activity row")
	require.Equal(t, expected.application, observed, "PostgreSQL activity must expose the exact safe operator label")
}

func lifecycleWaiterEnvironment(values ...string) []string {
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") || strings.HasPrefix(key, "LIFECYCLE_WAITER_") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, values...)
}

func lifecycleWaiterProbeConnectionString(t *testing.T, raw, kind string) (string, []string) {
	t.Helper()
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse only the generated fixture URL without publishing its connection string")
	query := target.Query()
	query.Set("application_name", "synthetic-uri-name")
	query.Set("APPLICATION_NAME", "synthetic-uppercase-name")
	query.Set("Application_Name", "synthetic-mixed-name")
	query.Set("statement_timeout", "20000")
	query.Set("lock_timeout", "10000")
	query.Set("search_path", "public")
	query.Set("options", "-c application_name=synthetic-options-name -c idle_in_transaction_session_timeout=25000")
	target.RawQuery = query.Encode()
	if kind == "uri" {
		return target.String(), nil
	}
	if kind == "query database" {
		query.Set("dbname", strings.TrimPrefix(target.Path, "/"))
		target.Path, target.RawPath, target.RawQuery = "/synthetic_unused", "", query.Encode()
		return target.String(), nil
	}
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), "'", `\'`) + "'"
	}
	password, _ := target.User.Password()
	port := target.Port()
	if port == "" {
		port = "5432"
	}
	if kind == "environment" {
		return "statement_timeout=20000 lock_timeout=10000 search_path=public", []string{"PGHOST=" + target.Hostname(), "PGPORT=" + port, "PGUSER=" + target.User.Username(), "PGPASSWORD=" + password, "PGDATABASE=" + strings.TrimPrefix(target.Path, "/"), "PGSSLMODE=" + query.Get("sslmode")}
	}
	require.Equal(t, "keyword", kind, "test only the declared native pgx DSN forms")
	return "host=" + quote(target.Hostname()) + " port=" + port + " user=" + quote(target.User.Username()) + " password=" + quote(password) + " dbname=" + quote(strings.TrimPrefix(target.Path, "/")) + " sslmode=" + quote(query.Get("sslmode")) + " application_name=synthetic-keyword-name APPLICATION_NAME=synthetic-uppercase-name Application_Name=synthetic-mixed-name statement_timeout=20000 lock_timeout=10000 search_path=public options='-c application_name=synthetic-options-name -c idle_in_transaction_session_timeout=25000'", nil
}

func newLifecycleWaiterPostgres(t *testing.T) (string, string) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema waiter proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the explicitly supplied disposable server without publishing its DSN")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(ctx), "close this fixture's administrator connection")
	})
	var serverVersion int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion), "read the actual disposable PostgreSQL version")
	require.GreaterOrEqual(t, serverVersion, 150000, "the complete schema requires PostgreSQL 15 or newer")
	name := "auxiliary_waiter_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this independently generated waiter fixture")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err, "drop only this generated database after owned connections close, without forced termination")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the supplied test URL without exposing connection details")
	query := target.Query()
	query.Del("dbname")
	query.Del("database")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	isolated := target.String()
	config, err := pgx.ParseConfig(isolated)
	require.True(t, err == nil && config.Database == name, "the inspection driver must select only the generated database")
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve the complete unmodified migration source")
	migration, err := migrate.New("file://"+source, isolated)
	require.True(t, err == nil, "open the actual migration driver on only the generated fixture")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close the migration source before database cleanup")
		require.NoError(t, databaseErr, "close the migration connection before database cleanup")
	})
	require.NoError(t, migration.Up(), "apply the entire unchanged schema including the passive writer registry")
	return isolated, name
}
