package postgres

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestTemplateKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		otherDSN    string
		changedSQL  bool
		expectEqual bool
	}{
		{name: "equivalent URL and keyword configuration", otherDSN: "host=localhost user=test password=secret dbname=fixtures sslmode=disable application_name=fixture", expectEqual: true},
		{name: "different database", otherDSN: "postgres://test:secret@localhost/other?sslmode=disable&application_name=fixture"},
		{name: "different role", otherDSN: "postgres://other:secret@localhost/fixtures?sslmode=disable&application_name=fixture"},
		{name: "different session defaults", otherDSN: "postgres://test:secret@localhost/fixtures?sslmode=disable&application_name=other"},
		{name: "changed migration contents", otherDSN: "postgres://test:secret@localhost/fixtures?sslmode=disable&application_name=fixture", changedSQL: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := writeMigrations(t)
			original, err := pgx.ParseConfig("postgres://test:secret@localhost/fixtures?sslmode=disable&application_name=fixture")
			require.NoError(t, err, "parse the original normalized fixture connection")
			first, err := templateKey(original, source)
			require.NoError(t, err, "fingerprint original migration contents and configuration")
			other, err := pgx.ParseConfig(tt.otherDSN)
			require.NoError(t, err, "parse the comparison fixture connection")
			if tt.changedSQL {
				require.NoError(t, os.WriteFile(filepath.Join(source, "000003_changed.up.sql"), []byte("ALTER TABLE fixture_marker ADD COLUMN changed boolean"), 0o600), "add a schema change without changing the migration directory")
			}
			second, err := templateKey(other, source)
			require.NoError(t, err, "fingerprint comparison migration contents and configuration")
			require.Equal(t, tt.expectEqual, first == second, "only identical schema and database identity should reuse a template")
		})
	}
}

func TestInvalidFixtureConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		dsn      string
		maxConns int32
		want     string
	}{
		{name: "invalid DSN omits credentials", dsn: "postgres://user:private-password@host/db?connect_timeout=invalid", want: "invalid TEST_DATABASE_URL"},
		{name: "negative pool size", dsn: "postgres://localhost/fixtures?sslmode=disable", maxConns: -1, want: "fixture maxConns must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var fixtures manager
			_, _, err := fixtures.newPool(context.Background(), tt.dsn, t.TempDir(), tt.maxConns)
			require.EqualError(t, err, tt.want, "invalid fixture configuration should fail before contacting PostgreSQL")
		})
	}
}

func TestTemplateClonesPostgres(t *testing.T) {
	t.Parallel()
	raw := postgresURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixtures := newManager(t)
	source := writeMigrations(t)
	defaultConfig, err := pgxpool.ParseConfig(raw)
	require.NoError(t, err, "parse the source pool defaults")
	type result struct {
		pool    *pgxpool.Pool
		cleanup func() error
		err     error
	}
	results := make(chan result, 3)
	for range 3 {
		go func() {
			pool, cleanup, err := fixtures.newPool(ctx, raw, source, 0)
			results <- result{pool, cleanup, err}
		}()
	}
	pools := make([]*pgxpool.Pool, 0, 3)
	for range 3 {
		got := <-results
		if got.cleanup != nil {
			t.Cleanup(func() { require.NoError(t, got.cleanup(), "remove each independently owned clone") })
		}
		require.NoError(t, got.err, "concurrent fixture setup should share one completed template")
		pools = append(pools, got.pool)
	}
	type snapshot struct {
		Database  string
		Schema    string
		Marker    string
		Extension string
		BaseTable bool
		Index     bool
	}
	actual := make([]snapshot, 0, len(pools))
	for _, pool := range pools {
		var got snapshot
		err := pool.QueryRow(ctx, `SELECT current_database(), current_schema(), value,
			(SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'),
			to_regclass('public.organizations') IS NOT NULL,
			(SELECT indisvalid FROM pg_index WHERE indexrelid='fixture_marker_value_idx'::regclass)
			FROM fixture_marker`).Scan(&got.Database, &got.Schema, &got.Marker, &got.Extension, &got.BaseTable, &got.Index)
		require.NoError(t, err, "read the migrated public schema from each clone")
		require.Equal(t, defaultConfig.MaxConns, pool.Config().MaxConns, "zero maxConns must preserve pgxpool defaults")
		actual = append(actual, got)
	}
	expected := make([]snapshot, 0, len(pools))
	for _, pool := range pools {
		expected = append(expected, snapshot{Database: pool.Config().ConnConfig.Database, Schema: "public", Marker: actual[0].Marker, Extension: "public", Index: true})
	}
	require.Equal(t, expected, actual, "clones should share the one migration-generated marker and complete public schema, excluding the base database")
	databaseNames := []string{actual[0].Database, actual[1].Database, actual[2].Database}
	sort.Strings(databaseNames)
	require.NotEqual(t, databaseNames[0], databaseNames[1], "concurrent fixtures must receive distinct databases")
	require.NotEqual(t, databaseNames[1], databaseNames[2], "all concurrent fixture databases must be distinct")
	_, err = pools[0].Exec(ctx, `UPDATE fixture_marker SET value='changed-in-first-clone'`)
	require.NoError(t, err, "mutate only the first clone")
	var unchanged string
	require.NoError(t, pools[1].QueryRow(ctx, `SELECT value FROM fixture_marker`).Scan(&unchanged), "read the independent second clone")
	require.Equal(t, actual[0].Marker, unchanged, "a fixture write must not change another clone or the template")
	fixtures.mu.Lock()
	entries := make([]*template, 0, len(fixtures.templates))
	for _, entry := range fixtures.templates {
		entries = append(entries, entry)
	}
	fixtures.mu.Unlock()
	require.Equal(t, 1, len(entries), "concurrent fixture requests should create exactly one migrated template")
	var acceptsConnections bool
	require.NoError(t, withAdmin(ctx, entries[0].config, func(admin *pgx.Conn) error {
		return admin.QueryRow(ctx, `SELECT datallowconn FROM pg_database WHERE datname=$1`, entries[0].name).Scan(&acceptsConnections)
	}), "inspect the immutable template without opening it")
	require.False(t, acceptsConnections, "the template should remain sealed between clone requests")
}

func TestFixtureConfigAndCleanupPostgres(t *testing.T) {
	t.Parallel()
	raw := postgresURL(t)
	tests := []struct {
		name          string
		maxConns      int32
		keyword       bool
		queryDatabase bool
	}{
		{name: "one connection pool", maxConns: 1},
		{name: "keyword DSN with a foreign search path", maxConns: 2, keyword: true},
		{name: "URL query database overrides the URL path", maxConns: 1, queryDatabase: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			fixtures := newManager(t)
			dsn := raw
			config, err := pgx.ParseConfig(raw)
			require.NoError(t, err, "parse the fixture control database")
			if tt.keyword {
				// ConnString returns the original DSN, not serialized mutated
				// configuration. Exercise keyword parsing and direct config copies.
				dsn = keywordDSN(config) + " search_path=nonexistent_fixture_schema"
			}
			if tt.queryDatabase {
				parsed, err := url.Parse(raw)
				require.NoError(t, err, "parse the disposable PostgreSQL URL")
				parsed.Path = "/nonexistent_fixture_source_database"
				query := parsed.Query()
				query.Set("dbname", config.Database)
				parsed.RawQuery = query.Encode()
				dsn = parsed.String()
			}
			pool, cleanup, err := fixtures.newPool(ctx, dsn, writeMigrations(t), tt.maxConns)
			require.NoError(t, err, "migrate and clone a database with the supplied connection configuration")
			t.Cleanup(func() { require.NoError(t, cleanup(), "idempotently remove the owned fixture") })
			var database, schema string
			require.NoError(t, pool.QueryRow(ctx, `SELECT current_database(),current_schema()`).Scan(&database, &schema), "connect to the clone rather than the original DSN database")
			require.Equal(t, []string{pool.Config().ConnConfig.Database, "public"}, []string{database, schema}, "both template migrations and fixture queries should use the isolated public schema")
			require.NotEqual(t, config.Database, database, "the source connection string must never override the cloned database")
			require.Equal(t, tt.maxConns, pool.Config().MaxConns, "explicit connection limits should survive cloning")
			require.NoError(t, cleanup(), "close the pool before dropping the owned fixture")
			require.False(t, databaseExists(t, ctx, config, database), "fixture cleanup should remove its complete database")
			require.NoError(t, fixtures.close(ctx), "suite cleanup should remove its sealed template")
			fixtures.mu.Lock()
			var names []string
			for _, entry := range fixtures.templates {
				names = append(names, entry.name)
			}
			fixtures.mu.Unlock()
			for _, name := range names {
				require.False(t, databaseExists(t, ctx, config, name), "suite cleanup should remove every owned template")
			}
		})
	}
}

func TestFailedMigrationIsRemovedPostgres(t *testing.T) {
	t.Parallel()
	raw := postgresURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixtures := newManager(t)
	source := t.TempDir()
	path := filepath.Join(source, "000001_fixture.up.sql")
	require.NoError(t, os.WriteFile(path, []byte("THIS IS NOT VALID SQL"), 0o600), "write a failing migration in the owned directory")
	_, _, err := fixtures.newPool(ctx, raw, source, 1)
	require.ErrorContains(t, err, "migrate fixture template", "migration failure should reach the test rather than be hidden in shared initialization")
	config, err := pgx.ParseConfig(raw)
	require.NoError(t, err, "parse the fixture control database")
	fixtures.mu.Lock()
	var failedNames []string
	for _, entry := range fixtures.templates {
		failedNames = append(failedNames, entry.name)
	}
	fixtures.mu.Unlock()
	for _, name := range failedNames {
		require.False(t, databaseExists(t, ctx, config, name), "failed migration initialization should remove its incomplete database immediately")
	}
	require.NoError(t, os.WriteFile(path, []byte("CREATE TABLE repaired_fixture(value integer)"), 0o600), "repair the migration contents at the same path")
	pool, cleanup, err := fixtures.newPool(ctx, raw, source, 1)
	require.NoError(t, err, "changed migration contents should initialize a new template instead of reusing a failed result")
	t.Cleanup(func() { require.NoError(t, cleanup(), "remove the clone of the repaired template") })
	var exists bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT to_regclass('public.repaired_fixture') IS NOT NULL`).Scan(&exists), "read the repaired schema from its clone")
	require.True(t, exists, "the new template should contain the repaired migration")
}

func newManager(t *testing.T) *manager {
	t.Helper()
	fixtures := &manager{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		require.NoError(t, fixtures.close(ctx), "remove all templates after the fixture cleanups finish")
	})
	return fixtures
}

func postgresURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL for real PostgreSQL fixture verification")
	}
	return raw
}

func writeMigrations(t *testing.T) string {
	t.Helper()
	source := t.TempDir()
	files := map[string]string{
		"000001_fixture.up.sql": "CREATE EXTENSION pgcrypto WITH SCHEMA public; CREATE TABLE fixture_marker(value text NOT NULL); INSERT INTO fixture_marker VALUES(md5(random()::text))",
		// A concurrent index must execute outside an implicit multi-statement
		// transaction, matching the repository's production migrations.
		"000002_fixture_index.up.sql": "CREATE INDEX CONCURRENTLY fixture_marker_value_idx ON fixture_marker(value)",
	}
	for name, sql := range files {
		require.NoError(t, os.WriteFile(filepath.Join(source, name), []byte(sql), 0o600), "write isolated fixture migration")
	}
	return source
}

func databaseExists(t *testing.T, ctx context.Context, config *pgx.ConnConfig, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, withAdmin(ctx, config, func(admin *pgx.Conn) error {
		return admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, name).Scan(&exists)
	}), "inspect only the named owned database")
	return exists
}

func keywordDSN(config *pgx.ConnConfig) string {
	quote := func(value string) string {
		return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(value) + "'"
	}
	return "host=" + quote(config.Host) + " port=" + quote(strconv.Itoa(int(config.Port))) +
		" user=" + quote(config.User) + " password=" + quote(config.Password) +
		" dbname=" + quote(config.Database) + " sslmode=disable"
}
