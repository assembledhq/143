package demoseed

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestReplaceDatabaseNameDriverTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "path target", raw: "postgres://tester@database.example.test/source?sslmode=disable"},
		{name: "query dbname target", raw: "postgres://tester@database.example.test/source?dbname=source&sslmode=disable"},
		{name: "query database target", raw: "postgres://tester@database.example.test/source?database=source&sslmode=disable"},
		{name: "both repeated target aliases", raw: "postgres://tester@database.example.test/source?dbname=source&dbname=another&database=source&database=another&sslmode=disable"},
		{name: "encoded target aliases", raw: "postgres://tester@database.example.test/so%75rce?db%6eame=source&data%62ase=source&sslmode=disable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := ReplaceDatabaseName(tt.raw, "generated_check")
			require.NoError(t, err, "replace every supported database selector before checking the seed")
			require.Equal(t, "postgres://tester@database.example.test/generated_check?sslmode=disable", actual, "only the generated sibling should remain as a database selector")
			config, err := pgx.ParseConfig(actual)
			require.NoError(t, err, "parse the pool's exact generated database URL")
			require.Equal(t, "generated_check", config.Database, "the seed pool must target the generated sibling")
			migrationDSN, err := pq.ParseURL(actual)
			require.NoError(t, err, "parse the migration driver's exact generated database URL")
			require.Equal(t, "dbname='generated_check' host='database.example.test' sslmode='disable' user='tester'", migrationDSN, "the migration driver must target the same generated sibling")
		})
	}
}

func TestCheckDatabaseTargetIsolationPostgres(t *testing.T) {
	t.Parallel()

	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to an owned disposable PostgreSQL 15+ server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to the disposable check-mode test server")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(closeCtx), "close the disposable database administrator")
	})
	var serverVersion int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&serverVersion), "read the test server version before applying the full schema")
	require.GreaterOrEqual(t, serverVersion, 150000, "the complete migration chain requires PostgreSQL 15 or newer")
	sourceName := "demo_source_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSource := pgx.Identifier{sourceName}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+quotedSource)
	require.NoError(t, err, "create an isolated synthetic source database")
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(dropCtx, "DROP DATABASE "+quotedSource+" WITH (FORCE)")
		require.NoError(t, err, "remove only the generated synthetic source database")
	})
	sourceURL, err := ReplaceDatabaseName(raw, sourceName)
	require.NoError(t, err, "select the generated synthetic source without inherited database aliases")
	source, err := pgx.Connect(ctx, sourceURL)
	require.NoError(t, err, "open the synthetic source database")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, source.Close(closeCtx), "close the synthetic source before database removal")
	})
	var actualSource string
	require.NoError(t, source.QueryRow(ctx, "SELECT current_database()").Scan(&actualSource), "verify the fixture database before its first write")
	require.Equal(t, sourceName, actualSource, "fixture writes must target only the generated synthetic source")
	_, err = source.Exec(ctx, "CREATE TABLE source_guard (id integer PRIMARY KEY, value text NOT NULL); INSERT INTO source_guard VALUES (1, 'preserve synthetic source')")
	require.NoError(t, err, "create the only source fixture table and its sentinel")

	parsed, err := url.Parse(sourceURL)
	require.NoError(t, err, "parse the synthetic source URL")
	query := parsed.Query()
	query["dbname"] = []string{sourceName, sourceName}
	query["database"] = []string{sourceName, sourceName}
	parsed.RawQuery = query.Encode()
	seedPath, err := filepath.Abs("../../.143/seed")
	require.NoError(t, err, "resolve the repository's synthetic preview seed")
	require.NoError(t, Check(ctx, CheckOptions{AdminDatabaseURL: parsed.String(), SeedPath: seedPath}), "check mode should migrate and seed only its generated sibling despite source query overrides")

	var sentinel string
	require.NoError(t, source.QueryRow(ctx, "SELECT value FROM source_guard WHERE id = 1").Scan(&sentinel), "read the untouched source sentinel after both seed passes")
	require.Equal(t, "preserve synthetic source", sentinel, "check mode must not alter existing source rows")
	var relations []string
	require.NoError(t, source.QueryRow(ctx, "SELECT array_agg(relkind::text || ':' || relname ORDER BY relname) FROM pg_class WHERE relnamespace = 'public'::regnamespace").Scan(&relations), "inspect all public source relations after the complete check")
	require.Equal(t, []string{"r:source_guard", "i:source_guard_pkey"}, relations, "migrations and seed writes must leave no new relations in the source database")
	var siblings []string
	require.NoError(t, admin.QueryRow(ctx, "SELECT COALESCE(array_agg(datname ORDER BY datname), ARRAY[]::text[]) FROM pg_database WHERE starts_with(datname::text, $1)", "oft_demo_seed_check_"+strconv.Itoa(os.Getpid())+"_").Scan(&siblings), "inspect temporary check databases after cleanup")
	require.Equal(t, []string{}, siblings, "check mode must remove its generated sibling after both successful seed passes")
}
