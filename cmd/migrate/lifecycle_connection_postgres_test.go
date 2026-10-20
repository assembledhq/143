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
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const lifecycleMigrationProbeBuild = "0123456789aBcDeF0123456789aBcDeF01234567"

func TestLifecycleMigrationConnectionCommandPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for real migration command attribution proofs")
	}
	// Reuse go test's default dependency build cache; the synthetic SHA only
	// changes linking and does not require a cold -trimpath compilation.
	binary := filepath.Join(t.TempDir(), "migrate")
	buildCtx, buildCancel := context.WithTimeout(context.Background(), time.Minute)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-ldflags", "-X github.com/assembledhq/143/internal/version.BuildSHA="+lifecycleMigrationProbeBuild, "-o", binary, ".")
	if err := build.Run(); err != nil {
		t.Fatalf("build the actual migration command with a synthetic binary SHA before starting its database proof (context status: %v)", buildCtx.Err())
	}
	tests := []struct {
		name, options, expectedIdleTimeout string
		queryDatabase                      bool
	}{
		{name: "URI options override environment options", options: "-c application_name=synthetic-uri-override -c idle_in_transaction_session_timeout=25000", expectedIdleTimeout: "25s"},
		{name: "query database target and environment options survive", expectedIdleTimeout: "26s", queryDatabase: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Up(), "apply the complete unchanged schema before running the real migration executable")
			baseline, dirty, err := migration.Version()
			require.NoError(t, err, "record the original full-schema migration ledger independently of the command's custom ledger")
			require.False(t, dirty, "the fully migrated fixture must start clean")
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			workingDirectory := newLifecycleMigrationProbeSource(t, ctx, actor)
			raw := lifecycleMigrationProbeURL(t, actor, tt.options, tt.queryDatabase)
			observer := newLifecycleWriterInstanceConnection(t, ctx, actor.Config())
			for _, direction := range []string{"up", "down"} {
				// These sequential directions intentionally share this case's own
				// migration ledger: down must reverse exactly the accepted up.
				lockTx, err := actor.Begin(ctx)
				require.NoError(t, err, "begin the test-owned lock transaction before launching each migration command")
				func() {
					defer func() { _ = lockTx.Rollback(ctx) }()
					_, err := lockTx.Exec(ctx, `LOCK TABLE lifecycle_migration_attribution_barrier IN ACCESS EXCLUSIVE MODE`)
					require.NoError(t, err, "hold the synthetic barrier until the real migrator backend is independently observed waiting")
					childCtx, childCancel := context.WithTimeout(ctx, 20*time.Second)
					child := exec.CommandContext(childCtx, binary, direction)
					child.Dir = workingDirectory
					child.Env = lifecycleMigrationProbeEnvironment(raw)
					var stdout, stderr bytes.Buffer
					child.Stdout, child.Stderr = &stdout, &stderr
					if err := child.Start(); err != nil {
						childCancel()
						t.Fatal("start only the stamped migration executable against its generated local fixture")
					}
					completed := make(chan error, 1)
					joined := make(chan struct{})
					go func() {
						defer close(joined)
						completed <- child.Wait()
					}()
					defer func() {
						childCancel()
						select {
						case <-joined:
						case <-time.After(5 * time.Second):
							t.Error("join the test-owned migration child before releasing database fixtures")
						}
					}()
					barrierCtx, barrierCancel := context.WithTimeout(ctx, 5*time.Second)
					defer barrierCancel()
					var backendPID int32
					for {
						err := observer.QueryRow(barrierCtx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND state='active' AND wait_event_type='Lock' AND query LIKE '%lifecycle_migration_attribution_barrier%' ORDER BY pid LIMIT 1),0)`, "143:operator:"+lifecycleMigrationProbeBuild).Scan(&backendPID)
						require.NoError(t, err, "inspect the independently blocked migration backend rather than assuming process startup completed")
						if backendPID != 0 {
							break
						}
						select {
						case <-barrierCtx.Done():
							t.Fatal("the actual libpq migration connection must expose the exact binary label at its confirmed lock wait")
						case <-time.After(10 * time.Millisecond):
						}
					}
					var observedName string
					require.NoError(t, observer.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE datname=current_database() AND pid=$1`, backendPID).Scan(&observedName), "inspect exactly the migration backend identified by the lock barrier")
					require.Equal(t, "143:operator:"+lifecycleMigrationProbeBuild, observedName, "the real command must use its linked SHA rather than URI, environment or options application-name overrides")
					require.NoError(t, lockTx.Rollback(ctx), "release the ordering barrier only after observing the actual migration connection")
					select {
					case err := <-completed:
						if err != nil {
							t.Fatal("the actual stamped migration command must finish its selected up or down operation")
						}
					case <-childCtx.Done():
						t.Fatal("the unblocked migration command must finish within its test-owned deadline")
					}
					wantOutput := "[migrate] 1/u attribution (<duration>)\nMigrations applied successfully.\n"
					if direction == "down" {
						wantOutput = "[migrate] 1/d attribution (<duration>)\nMigrations rolled back successfully.\n"
					}
					// Never dump captured command output or connection strings in
					// public CI output, including when a regression causes failure.
					output, valid := normalizeLifecycleMigrationProbeOutput(stdout.String())
					require.True(t, valid, "the real command must retain exactly the pinned driver's normal timing log and success framing")
					require.True(t, output == wantOutput, "passive naming must preserve every static output byte while normalizing only the parsed migration duration")
					require.Zero(t, stderr.Len(), "passive naming must not add diagnostics to a successful migration")
					assertLifecycleMigrationProbe(t, ctx, actor, direction, "143:operator:"+lifecycleMigrationProbeBuild, tt.expectedIdleTimeout, backendPID)
				}()
			}
			assertLifecycleMigrationLedgers(t, ctx, actor, migration, baseline)
		})
	}
}

func TestLifecycleMigrationConnectionDriverPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, build, expectedName string
	}{
		{name: "development binary", build: "dev", expectedName: "143:operator:dev"},
		{name: "non-SHA build is a marked fingerprint", build: "build-a", expectedName: "143:operator:sha256-5e616f6135eb478ecd04b3d3df35b56e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, actor := newContentMigrationPostgres(t, false)
			require.NoError(t, migration.Up(), "apply the full schema before testing the migration URL helper through the real libpq driver")
			baseline, dirty, err := migration.Version()
			require.NoError(t, err, "record the original full-schema ledger before a separate synthetic migration")
			require.False(t, dirty, "driver-path probes must start from a clean full-schema fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			workingDirectory := newLifecycleMigrationProbeSource(t, ctx, actor)
			raw := lifecycleMigrationProbeURL(t, actor, "-c application_name=synthetic-uri-override -c idle_in_transaction_session_timeout=25000", true)
			attributed := lifecycleMigrationDatabaseURL(raw, tt.build)
			source := "file://" + filepath.Join(workingDirectory, "migrations")
			driver, err := migrate.New(source, attributed)
			require.NoError(t, err, "open the actual golang-migrate postgres/libpq driver with the attributed URL")
			t.Cleanup(func() {
				sourceErr, databaseErr := driver.Close()
				require.NoError(t, sourceErr, "close only the synthetic migration source")
				require.NoError(t, databaseErr, "close only the real driver connection used by the attribution probe")
			})
			require.NoError(t, driver.Up(), "run synthetic up through golang-migrate rather than merely parsing the transformed URL")
			var backendPID int32
			require.NoError(t, actor.QueryRow(ctx, `SELECT backend_pid FROM lifecycle_migration_attribution_probe WHERE direction='up'`).Scan(&backendPID), "obtain the exact real driver backend from its synthetic migration audit")
			var observedName string
			require.NoError(t, actor.QueryRow(ctx, `SELECT application_name FROM pg_stat_activity WHERE datname=current_database() AND pid=$1`, backendPID).Scan(&observedName), "independently inspect the actual migration driver's retained libpq backend")
			require.Equal(t, tt.expectedName, observedName, "development and fingerprint attribution must reach the real migration driver connection")
			assertLifecycleMigrationProbe(t, ctx, actor, "up", tt.expectedName, "25s", backendPID)
			require.NoError(t, driver.Down(), "reverse only the synthetic migration through the same unchanged driver path")
			assertLifecycleMigrationProbe(t, ctx, actor, "down", tt.expectedName, "25s", backendPID)
			assertLifecycleMigrationLedgers(t, ctx, actor, migration, baseline)
		})
	}
}

func newLifecycleMigrationProbeSource(t *testing.T, ctx context.Context, actor *pgx.Conn) string {
	t.Helper()
	_, err := actor.Exec(ctx, `CREATE TABLE lifecycle_migration_attribution_barrier(id integer PRIMARY KEY); CREATE TABLE lifecycle_migration_attribution_probe(direction text PRIMARY KEY,backend_pid integer NOT NULL,database_name text NOT NULL,application_name text NOT NULL,writer_version text,statement_timeout text NOT NULL,lock_timeout text NOT NULL,search_path text NOT NULL,idle_timeout text NOT NULL)`)
	require.NoError(t, err, "create only synthetic ordering and audit tables in the independently generated test database")
	root := t.TempDir()
	directory := filepath.Join(root, "migrations")
	require.NoError(t, os.Mkdir(directory, 0700), "create an isolated source directory for the actual migration command")
	for _, direction := range []string{"up", "down"} {
		statement := "CREATE TABLE lifecycle_migration_attribution_effect(id integer PRIMARY KEY);"
		if direction == "down" {
			statement = "DROP TABLE lifecycle_migration_attribution_effect;"
		}
		migration := `SELECT id FROM lifecycle_migration_attribution_barrier;
INSERT INTO lifecycle_migration_attribution_probe SELECT '` + direction + `',pg_backend_pid(),current_database(),current_setting('application_name'),current_setting('lifecycle143.writer_version',true),current_setting('statement_timeout'),current_setting('lock_timeout'),current_setting('search_path'),current_setting('idle_in_transaction_session_timeout');
` + statement
		require.NoError(t, os.WriteFile(filepath.Join(directory, "1_attribution."+direction+".sql"), []byte(migration), 0600), "write only synthetic migration SQL in the private temporary fixture directory")
	}
	return root
}

func lifecycleMigrationProbeURL(t *testing.T, actor *pgx.Conn, options string, queryDatabase bool) string {
	t.Helper()
	target, err := url.Parse(actor.Config().ConnString())
	if err != nil {
		t.Fatal("parse the explicit generated test database target without printing its connection string")
	}
	query := target.Query()
	query.Set("application_name", "synthetic-old-name")
	query.Set("APPLICATION_NAME", "synthetic-uppercase-override")
	query.Set("Application_Name", "synthetic-mixed-case-override")
	query.Set("fallback_application_name", "synthetic-fallback-name")
	query.Set("statement_timeout", "20000")
	query.Set("lock_timeout", "10000")
	query.Set("search_path", "public")
	query.Set("x-migrations-table", "synthetic_attribution_history")
	query.Set("x-statement-timeout", "15000")
	if options == "" {
		query.Del("options")
	} else {
		query.Set("options", options)
	}
	if queryDatabase {
		query.Set("dbname", actor.Config().Database)
		// lib/pq sorts duplicate dbname clauses lexically rather than using a
		// universal query-over-path rule. Exercise the query target on its own
		// instead of assuming which of two conflicting targets wins.
		target.Path, target.RawPath = "", ""
	}
	target.RawQuery = query.Encode()
	return target.String()
}

func normalizeLifecycleMigrationProbeOutput(output string) (string, bool) {
	// golang-migrate v4.19.1 always emits its normal timing record when Log is
	// configured, even with verbose logging disabled. Preserve that record and
	// the exact success line; only its time.Duration.String value is variable.
	lines := strings.Split(output, "\n")
	if len(lines) != 3 || lines[2] != "" {
		return "", false
	}
	prefix := ""
	switch lines[1] {
	case "Migrations applied successfully.":
		prefix = "[migrate] 1/u attribution ("
	case "Migrations rolled back successfully.":
		prefix = "[migrate] 1/d attribution ("
	default:
		return "", false
	}
	if !strings.HasPrefix(lines[0], prefix) || !strings.HasSuffix(lines[0], ")") {
		return "", false
	}
	rawDuration := strings.TrimSuffix(strings.TrimPrefix(lines[0], prefix), ")")
	duration, err := time.ParseDuration(rawDuration)
	if err != nil || duration < 0 || duration.String() != rawDuration {
		return "", false
	}
	return prefix + "<duration>)\n" + lines[1] + "\n", true
}

func TestNormalizeLifecycleMigrationProbeOutput(t *testing.T) {
	t.Parallel()
	const up = "[migrate] 1/u attribution (<duration>)\nMigrations applied successfully.\n"
	const down = "[migrate] 1/d attribution (<duration>)\nMigrations rolled back successfully.\n"
	tests := []struct {
		name, output, expected string
		valid                  bool
	}{
		{name: "up milliseconds", output: "[migrate] 1/u attribution (12.345ms)\nMigrations applied successfully.\n", expected: up, valid: true},
		{name: "down microseconds", output: "[migrate] 1/d attribution (12µs)\nMigrations rolled back successfully.\n", expected: down, valid: true},
		{name: "zero duration", output: "[migrate] 1/u attribution (0s)\nMigrations applied successfully.\n", expected: up, valid: true},
		{name: "success alone is insufficient", output: "Migrations applied successfully.\n"},
		{name: "wrong direction framing", output: "[migrate] 1/d attribution (1ms)\nMigrations applied successfully.\n"},
		{name: "wrong migration identifier", output: "[migrate] 1/u different (1ms)\nMigrations applied successfully.\n"},
		{name: "negative duration", output: "[migrate] 1/u attribution (-1ms)\nMigrations applied successfully.\n"},
		{name: "missing duration unit", output: "[migrate] 1/u attribution (12)\nMigrations applied successfully.\n"},
		{name: "noncanonical duration", output: "[migrate] 1/u attribution (1.000ms)\nMigrations applied successfully.\n"},
		{name: "additional output", output: "[migrate] 1/u attribution (1ms)\nMigrations applied successfully.\nunexpected\n"},
		{name: "additional blank line", output: "[migrate] 1/u attribution (1ms)\nMigrations applied successfully.\n\n"},
		{name: "missing final newline", output: "[migrate] 1/u attribution (1ms)\nMigrations applied successfully."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, valid := normalizeLifecycleMigrationProbeOutput(tt.output)
			require.Equal(t, tt.valid, valid, "only the exact pinned migration framing with a canonical nonnegative duration may be normalized")
			require.Equal(t, tt.expected, actual, "normalization must replace only the duration and preserve every static output byte")
		})
	}
}

func lifecycleMigrationProbeEnvironment(databaseURL string) []string {
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DATABASE_URL", "PGAPPNAME", "PGOPTIONS", "PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "LOG_LEVEL":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "DATABASE_URL="+databaseURL, "PGAPPNAME=synthetic-environment-name", "PGOPTIONS=-c application_name=synthetic-environment-override -c idle_in_transaction_session_timeout=26000", "LOG_LEVEL=info")
}

func assertLifecycleMigrationProbe(t *testing.T, ctx context.Context, actor *pgx.Conn, direction, applicationName, idleTimeout string, backendPID int32) {
	t.Helper()
	type observation struct {
		Direction, DatabaseName, ApplicationName string
		BackendPID                               int32
		WriterVersion                            *string
		StatementTimeout, LockTimeout            string
		SearchPath, IdleTimeout                  string
	}
	var got observation
	err := actor.QueryRow(ctx, `SELECT direction,backend_pid,database_name,application_name,writer_version,statement_timeout,lock_timeout,search_path,idle_timeout FROM lifecycle_migration_attribution_probe WHERE direction=$1`, direction).Scan(&got.Direction, &got.BackendPID, &got.DatabaseName, &got.ApplicationName, &got.WriterVersion, &got.StatementTimeout, &got.LockTimeout, &got.SearchPath, &got.IdleTimeout)
	require.NoError(t, err, "read every same-connection setting recorded by the actual migration SQL")
	require.Equal(t, observation{Direction: direction, DatabaseName: actor.Config().Database, ApplicationName: applicationName, BackendPID: backendPID, StatementTimeout: "20s", LockTimeout: "10s", SearchPath: "public", IdleTimeout: idleTimeout}, got, "attribution must preserve database selection and every unrelated runtime option without installing a lifecycle writer marker")
}

func assertLifecycleMigrationLedgers(t *testing.T, ctx context.Context, actor *pgx.Conn, migration *migrate.Migrate, baseline uint) {
	t.Helper()
	version, dirty, err := migration.Version()
	require.NoError(t, err, "reload the original full-schema ledger independently of the custom migration table")
	require.Equal(t, baseline, version, "custom-table up/down must never alter the original repository schema ledger")
	require.False(t, dirty, "the repository schema ledger must remain clean after the independent probe")
	var got []int
	err = actor.QueryRow(ctx, `SELECT ARRAY[(SELECT count(*)::integer FROM synthetic_attribution_history),(SELECT count(*)::integer FROM lifecycle_writer_instances),(SELECT count(*)::integer FROM lifecycle_migration_attribution_probe)]`).Scan(&got)
	require.NoError(t, err, "inspect the custom migration ledger, unchanged passive writer registry and synthetic audit separately")
	require.Equal(t, []int{0, 0, 2}, got, "down must clear only the custom migration ledger, retain both synthetic direction audits and create no writer observations")
	var effectExists bool
	require.NoError(t, actor.QueryRow(ctx, `SELECT to_regclass('public.lifecycle_migration_attribution_effect') IS NOT NULL`).Scan(&effectExists), "inspect the synthetic up effect independently of migration success messages")
	require.False(t, effectExists, "the actual down migration must reverse the synthetic up effect")
}
