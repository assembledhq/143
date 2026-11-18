package demoseed

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/version"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestDemoSeedLifecyclePoolPostgres(t *testing.T) {
	t.Parallel()

	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to an owned disposable PostgreSQL server")
	}
	tests := []struct {
		name   string
		before func(context.Context, *pgxpool.Pool) error
	}{
		{name: "initial pool connection"},
		{name: "reused connection", before: func(ctx context.Context, pool *pgxpool.Pool) error { return pool.Ping(ctx) }},
		{name: "reset connection", before: func(_ context.Context, pool *pgxpool.Pool) error { pool.Reset(); return nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			target, err := url.Parse(raw)
			require.NoError(t, err, "parse the disposable server URL without changing its target")
			query, err := url.ParseQuery(target.RawQuery)
			require.NoError(t, err, "preserve valid disposable-server query parameters")
			query.Set("application_name", "synthetic-original")
			query.Set("APPLICATION_NAME", "synthetic-uppercase")
			query.Set("Application_Name", "synthetic-mixed")
			query.Set("options", "-c application_name=synthetic-option -c idle_in_transaction_session_timeout=25000")
			query.Set("statement_timeout", "17000")
			query.Set("lock_timeout", "13000")
			query.Set("search_path", "pg_catalog")
			query.Set("pool_max_conns", "1")
			target.RawQuery = query.Encode()
			baseline, err := pgxpool.ParseConfig(target.String())
			require.NoError(t, err, "parse the native fixture target before opening the attributed pool")
			pool, err := connectPool(ctx, target.String())
			require.NoError(t, err, "open the production demo pool with passive attribution")
			t.Cleanup(pool.Close)
			if tt.before != nil {
				require.NoError(t, tt.before(ctx, pool), "prepare the selected connection lifecycle without changing its configuration")
			}
			type snapshot struct {
				application, database, statement, lock, idle, searchPath string
				writerVersion                                            *string
			}
			var actual snapshot
			err = pool.QueryRow(ctx, "SELECT current_setting('application_name'), current_database(), current_setting('statement_timeout'), current_setting('lock_timeout'), current_setting('idle_in_transaction_session_timeout'), current_setting('search_path'), current_setting('lifecycle143.writer_version', true)").Scan(&actual.application, &actual.database, &actual.statement, &actual.lock, &actual.idle, &actual.searchPath, &actual.writerVersion)
			require.NoError(t, err, "read the effective settings from the attributed demo connection")
			expected := snapshot{
				application: db.LifecycleOperatorConnectionApplicationName(version.BuildSHA),
				database:    baseline.ConnConfig.Database, statement: "17s", lock: "13s", idle: "25s", searchPath: "pg_catalog",
			}
			require.Equal(t, expected, actual, "the safe binary label must win every app-name alias while native targets and unrelated options survive without a writer marker")
			_, err = pool.Exec(ctx, "SELECT 1; SELECT 2")
			require.NoError(t, err, "the attributed seed pool must retain multi-statement simple-protocol execution")
		})
	}
}

func TestDemoSeedLifecycleEmbeddedMigrationPostgres(t *testing.T) {
	t.Parallel()

	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to an owned disposable PostgreSQL server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to the owned disposable migration test server")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(closeCtx), "close the disposable migration administrator")
	})
	name := "demo_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create an isolated synthetic migration database")
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(dropCtx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err, "remove only the generated embedded-migration database")
	})
	isolated, err := ReplaceDatabaseName(raw, name)
	require.NoError(t, err, "select only the generated embedded-migration database")
	conn, err := pgx.Connect(ctx, isolated)
	require.NoError(t, err, "open the isolated migration inspection connection")
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, conn.Close(closeCtx), "close migration inspection before removing its database")
	})
	var actualDatabase string
	require.NoError(t, conn.QueryRow(ctx, "SELECT current_database()").Scan(&actualDatabase), "verify the isolated migration target before fixture writes")
	require.Equal(t, name, actualDatabase, "migration fixture writes must never target the supplied admin database")
	latest := latestDemoMigrationVersion(t)
	_, err = conn.Exec(ctx, "CREATE TABLE synthetic_migration_state (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)")
	require.NoError(t, err, "create a custom synthetic ledger without applying the full schema")
	_, err = conn.Exec(ctx, "INSERT INTO synthetic_migration_state (version, dirty) VALUES ($1, false)", latest)
	require.NoError(t, err, "mark the fixture at the repository's existing latest version")
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "start the migration-driver observation barrier")
	_, err = tx.Exec(ctx, "LOCK TABLE synthetic_migration_state IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err, "hold the custom ledger until the real embedded driver is observed")
	finished := make(chan struct{})
	var migrationErr error
	started := false
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanupCtx); !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			require.NoError(t, rollbackErr, "release the migration barrier before waiting for driver cleanup")
		}
		if !started {
			return
		}
		select {
		case <-finished:
			require.NoError(t, migrationErr, "the actual embedded migrator must finish without changing the synthetic ledger")
		case <-cleanupCtx.Done():
			t.Error("embedded migration driver did not finish after its ledger barrier was released")
		}
	})
	target, err := url.Parse(isolated)
	require.NoError(t, err, "parse the isolated driver's fixture URL")
	query := target.Query()
	query.Set("dbname", name)
	query.Set("application_name", "synthetic-original")
	query.Set("APPLICATION_NAME", "synthetic-uppercase")
	query.Set("Application_Name", "synthetic-mixed")
	query.Set("options", "-c application_name=synthetic-option -c idle_in_transaction_session_timeout=25000")
	query.Set("x-migrations-table", "synthetic_migration_state")
	target.RawQuery = query.Encode()
	started = true
	go func() {
		migrationErr = RunMigrations(target.String())
		close(finished)
	}()
	var observed []string
	var observationErr error
	// Keep observation synchronous: an Eventually condition can outlive its
	// deadline and race cleanup on the administrator's non-concurrent pgx.Conn.
	observationCtx, observationCancel := context.WithTimeout(ctx, 10*time.Second)
	defer observationCancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
observe:
	for {
		observationErr = admin.QueryRow(observationCtx, "SELECT COALESCE(array_agg(application_name::text || '|' || datname || '|' || backend_type ORDER BY pid), ARRAY[]::text[]) FROM pg_stat_activity WHERE datname = $1 AND $2 = ANY(pg_blocking_pids(pid))", name, conn.PgConn().PID()).Scan(&observed)
		if observationErr != nil || len(observed) != 0 {
			break
		}
		select {
		case <-observationCtx.Done():
			observationErr = observationCtx.Err()
			break observe
		case <-ticker.C:
		}
	}
	require.NoError(t, observationErr, "the independent observer must read only the generated migration backend")
	require.Equal(t, []string{db.LifecycleOperatorConnectionApplicationName(version.BuildSHA) + "|" + name + "|client backend"}, observed, "the embedded migration driver must receive the binary's safe operator label without changing its selected database")
	require.NoError(t, tx.Rollback(ctx), "release the observed ledger barrier")
	select {
	case <-finished:
		require.NoError(t, migrationErr, "an already-current embedded migration must retain no-change success behavior")
	case <-ctx.Done():
		t.Fatal("embedded migration did not finish within its fixture deadline")
	}
	var ledger []string
	require.NoError(t, conn.QueryRow(ctx, "SELECT array_agg(version::text || ':' || dirty::text ORDER BY version) FROM synthetic_migration_state").Scan(&ledger), "inspect the exact custom migration ledger after no-change completion")
	require.Equal(t, []string{strconv.FormatInt(latest, 10) + ":false"}, ledger, "passive attribution must preserve the existing clean custom migration ledger")
	var tables []string
	require.NoError(t, conn.QueryRow(ctx, "SELECT array_agg(tablename::text ORDER BY tablename) FROM pg_tables WHERE schemaname = 'public'").Scan(&tables), "inspect the synthetic migration database's complete table set")
	require.Equal(t, []string{"synthetic_migration_state"}, tables, "attribution must not introduce a default ledger, observer table or schema authority")
}

func latestDemoMigrationVersion(t *testing.T) int64 {
	t.Helper()

	source, err := ResolveMigrationSource()
	require.NoError(t, err, "resolve the same unchanged migration source as the embedded driver")
	entries, err := os.ReadDir(strings.TrimPrefix(source, "file://"))
	require.NoError(t, err, "read repository migration names without applying them")
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		prefix, _, found := strings.Cut(entry.Name(), "_")
		require.True(t, found, "each repository migration must retain its existing numeric prefix")
		candidate, err := strconv.ParseInt(prefix, 10, 64)
		require.NoError(t, err, "parse an existing repository migration version")
		if candidate > latest {
			latest = candidate
		}
	}
	require.Positive(t, latest, "the synthetic current ledger must refer to an existing repository migration")
	return latest
}
