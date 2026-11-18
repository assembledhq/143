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

func TestBackfillUsageConnectionPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("LIFECYCLE_AUXILIARY_CHILD") == "backfill" {
		requireBackfillUsageConnectionState(t)
		return
	}
	raw, database := newAuxiliaryCommandPostgres(t)
	tests := []struct {
		name, kind, build, expected string
	}{
		{name: "URI aliases and options", kind: "uri", build: lifecycleAuxiliaryProbeBuild, expected: "143:operator:" + lifecycleAuxiliaryProbeBuild},
		{name: "query database target", kind: "query database", build: lifecycleAuxiliaryProbeBuild, expected: "143:operator:" + lifecycleAuxiliaryProbeBuild},
		{name: "keyword aliases and options", kind: "keyword", build: "dev", expected: "143:operator:dev"},
		{name: "environment connection defaults", kind: "environment", build: lifecycleAuxiliaryProbeBuild, expected: "143:operator:" + lifecycleAuxiliaryProbeBuild},
		{name: "unsafe metadata fingerprint", kind: "uri", build: "build-a", expected: "143:operator:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			connectionString, defaults := auxiliaryProbeConnectionString(t, raw, tt.kind)
			expectedIdle := "25s"
			if tt.kind == "environment" {
				expectedIdle = "26s"
			}
			values := append([]string{
				"LIFECYCLE_AUXILIARY_CHILD=backfill", "LIFECYCLE_AUXILIARY_URL=" + connectionString,
				"LIFECYCLE_AUXILIARY_DATABASE=" + database, "LIFECYCLE_AUXILIARY_BUILD=" + tt.build,
				"LIFECYCLE_AUXILIARY_EXPECTED=" + tt.expected, "PGAPPNAME=synthetic-environment-name",
				"LIFECYCLE_AUXILIARY_IDLE=" + expectedIdle,
				"PGOPTIONS=-c application_name=synthetic-environment-options -c idle_in_transaction_session_timeout=26000",
			}, defaults...)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBackfillUsageConnectionPostgres$", "-test.timeout=15s")
			child.Env = auxiliaryCommandEnvironment(values...)
			var output bytes.Buffer
			child.Stdout, child.Stderr = &output, &output
			require.True(t, child.Run() == nil, "the actual backfill factory must pass its isolated-process PostgreSQL target, naming, setting and passive-authority proofs")
		})
	}
}

func requireBackfillUsageConnectionState(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := newBackfillUsagePool(ctx, os.Getenv("LIFECYCLE_AUXILIARY_URL"), os.Getenv("LIFECYCLE_AUXILIARY_BUILD"))
	require.True(t, err == nil, "open the actual backfill pool without publishing its connection error or DSN")
	defer pool.Close()
	config := pool.Config()
	require.Equal(t, int32(3), config.MaxConns, "passive attribution must retain the caller's explicit maximum pool capacity")
	require.Equal(t, int32(0), config.MinConns, "passive attribution must preserve the caller's explicit minimum pool size")
	require.Equal(t, 23*time.Minute, config.MaxConnIdleTime, "passive attribution must retain the caller's explicit idle-time setting")
	connection, err := pool.Acquire(ctx)
	require.NoError(t, err, "acquire an actual production-factory connection")
	defer connection.Release()
	type state struct {
		database, application, statementTimeout, lockTimeout, searchPath, idleTimeout string
		writerVersion                                                                 *string
		registryRows                                                                  int
	}
	actual := state{}
	err = connection.QueryRow(ctx, `SELECT current_database(),current_setting('application_name'),current_setting('statement_timeout'),current_setting('lock_timeout'),current_setting('search_path'),current_setting('idle_in_transaction_session_timeout'),current_setting('lifecycle143.writer_version',true),(SELECT count(*) FROM lifecycle_writer_instances)`).Scan(&actual.database, &actual.application, &actual.statementTimeout, &actual.lockTimeout, &actual.searchPath, &actual.idleTimeout, &actual.writerVersion, &actual.registryRows)
	require.NoError(t, err, "read real PostgreSQL settings and the full-schema writer registry on the acquired connection")
	expected := state{database: os.Getenv("LIFECYCLE_AUXILIARY_DATABASE"), application: os.Getenv("LIFECYCLE_AUXILIARY_EXPECTED"), statementTimeout: "20s", lockTimeout: "10s", searchPath: "public", idleTimeout: os.Getenv("LIFECYCLE_AUXILIARY_IDLE")}
	require.Equal(t, expected, actual, "only application naming may change: target, unrelated GUCs and empty passive-authority state must remain exact")
	var observed string
	require.NoError(t, connection.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&observed), "read the exact factory-created backend rather than every process sharing its build")
	require.Equal(t, expected.application, observed, "the backend's activity row must expose the same safe operator label")
}

func newAuxiliaryCommandPostgres(t *testing.T) (string, string) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for full-schema auxiliary-command proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the explicitly supplied disposable PostgreSQL server without logging its DSN")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(ctx), "close this fixture's administrator connection")
	})
	var serverVersion int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion), "read the actual disposable server version before creating any fixture")
	require.GreaterOrEqual(t, serverVersion, 150000, "the unchanged complete schema requires PostgreSQL 15 or newer")
	name := "auxiliary_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create only this test's independently generated database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err, "remove only the generated fixture after all child connections close, without forced termination")
	})
	target, err := url.Parse(raw)
	require.True(t, err == nil, "parse the supplied fixture URL without publishing its contents")
	query := target.Query()
	query.Del("dbname")
	query.Del("database")
	target.Path, target.RawPath, target.RawQuery = "/"+name, "", query.Encode()
	isolated := target.String()
	config, err := pgx.ParseConfig(isolated)
	require.True(t, err == nil && config.Database == name, "both the fixture and migrator must select only the generated database")
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "locate the unchanged repository migration source")
	migration, err := migrate.New("file://"+source, isolated)
	require.True(t, err == nil, "open the real migration driver against only the generated fixture")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close the repository migration source before dropping the fixture")
		require.NoError(t, databaseErr, "close the fixture's migration connection before dropping its database")
	})
	require.NoError(t, migration.Up(), "apply the entire real schema, including the passive writer registry")
	return isolated, name
}
