package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// migrateLogger implements migrate.Logger to surface verbose migration output.
type migrateLogger struct{ verbose bool }

const (
	prReadinessDirtyMigrationVersion                 = 267
	codeReviewDisputesDirtyMigrationVersion          = 281
	sandboxWorkloadRoutingMigrationVersion           = 286
	sandboxWorkloadRoutingValidationMigrationVersion = 287
)

type dirtyMigrationRepairer interface {
	Version() (uint, bool, error)
	Force(version int) error
}

type migrationVersionReader interface {
	Version() (uint, bool, error)
}

type migrationPreparationConn interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type concurrentIndexPreparation struct {
	name      string
	createSQL string
}

const sandboxWorkloadBackfillSQL = `WITH active_sandbox_jobs AS MATERIALIZED (
		SELECT j.id,
			j.org_id,
			CASE
				WHEN j.payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
				THEN (j.payload->>'session_id')::uuid
			END AS session_id
		FROM jobs j
		WHERE j.job_type IN ('run_agent', 'continue_session')
		  AND j.status IN ('pending', 'running')
		  AND j.workload_class <> 'code_review'
	)
	UPDATE jobs j
	SET workload_class = 'code_review'
	FROM active_sandbox_jobs active
	JOIN sessions s
	  ON s.id = active.session_id
	 AND s.org_id = active.org_id
	WHERE j.id = active.id
	  AND j.org_id = active.org_id
	  AND s.origin = 'code_review'`

var sandboxRoutingConcurrentIndexes = []concurrentIndexPreparation{
	{
		name: "idx_jobs_sandbox_routing",
		createSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jobs_sandbox_routing
			ON jobs (
				priority DESC,
				(CASE WHEN workload_class = 'interactive' THEN 0 ELSE 1 END),
				run_at ASC,
				created_at ASC
			)
			WHERE status = 'pending'
			  AND job_type IN ('run_agent', 'continue_session')`,
	},
	{
		name: "idx_jobs_active_sandbox_turns",
		createSQL: `CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jobs_active_sandbox_turns
			ON jobs (org_id, status)
			WHERE job_type IN ('run_agent', 'continue_session')
			  AND status IN ('pending', 'running')`,
	},
}

func (l migrateLogger) Printf(format string, v ...interface{}) {
	fmt.Printf("[migrate] "+format, v...)
}

func (l migrateLogger) Verbose() bool { return l.verbose }

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://onefortythree:dev@localhost:5432/onefortythree?sslmode=disable" // #nosec G101 -- dev-only default, not a credential
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: migrate [up|down|repair-known-dirty|repair-pr-readiness]")
		os.Exit(1)
	}

	migrationSource, err := resolveMigrationSource(pathExists)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve migrations directory: %v\n", err)
		os.Exit(1)
	}

	m, err := migrate.New(migrationSource, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create migrator: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	m.Log = migrateLogger{verbose: os.Getenv("LOG_LEVEL") == "debug"}

	switch os.Args[1] {
	case "up":
		repaired, err := repairSandboxWorkloadRoutingDirtyMigration(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to repair dirty sandbox workload routing migration: %v\n", err)
			os.Exit(1)
		}
		if repaired {
			fmt.Println("Repaired dirty sandbox workload routing migration; replaying routing migrations.")
		}
		preparationRequired, err := sandboxWorkloadRoutingPreparationRequired(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to inspect sandbox workload routing migration state: %v\n", err)
			os.Exit(1)
		}
		if preparationRequired {
			if err := prepareSandboxWorkloadRouting(context.Background(), dbURL); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to prepare sandbox workload routing migration: %v\n", err)
				os.Exit(1)
			}
		}
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			logMigrationError("up", m, err)
			os.Exit(1)
		}
		fmt.Println("Migrations applied successfully.")
	case "down":
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			logMigrationError("down", m, err)
			os.Exit(1)
		}
		fmt.Println("Migrations rolled back successfully.")
	case "repair-known-dirty":
		version, repaired, err := repairKnownDirtyMigration(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to repair known dirty migration state: %v\n", err)
			os.Exit(1)
		}
		if repaired {
			fmt.Printf("Repaired dirty migration %d; migrations can resume from %d.\n", version, version-1)
		} else {
			fmt.Println("Known dirty migration repair not needed.")
		}
	case "repair-pr-readiness":
		repaired, err := repairPRReadinessDirtyMigration(m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to repair PR readiness migration state: %v\n", err)
			os.Exit(1)
		}
		if repaired {
			fmt.Printf("Repaired dirty migration %d; migrations can resume from %d.\n", prReadinessDirtyMigrationVersion, prReadinessDirtyMigrationVersion-1)
		} else {
			fmt.Println("PR readiness migration repair not needed.")
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1]) // #nosec G705 -- writing to stderr, not HTTP response
		os.Exit(1)
	}
}

func sandboxWorkloadRoutingPreparationRequired(reader migrationVersionReader) (bool, error) {
	version, dirty, err := reader.Version()
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			return true, nil
		}
		return false, fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return false, fmt.Errorf("database is dirty at migration %d; repair it before sandbox workload routing preparation", version)
	}
	if version < sandboxWorkloadRoutingMigrationVersion {
		return true, nil
	}
	return false, nil
}

// prepareSandboxWorkloadRouting performs the non-transactional portion of the
// hot jobs-table migration. Production deploys and provisions already invoke
// `migrate up`, so keeping the preparation here makes the staged rollout
// executable instead of relying on an operator to copy SQL from a comment.
func prepareSandboxWorkloadRouting(ctx context.Context, dbURL string) (resultErr error) {
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect for sandbox workload routing preparation: %w", err)
	}
	defer func() {
		if err := conn.Close(context.WithoutCancel(ctx)); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("close sandbox workload routing preparation connection: %w", err)
		}
	}()
	return prepareSandboxWorkloadRoutingOnConn(ctx, conn)
}

func prepareSandboxWorkloadRoutingOnConn(ctx context.Context, conn migrationPreparationConn) error {
	var schemaReady bool
	if err := conn.QueryRow(ctx, `
		SELECT to_regclass('public.jobs') IS NOT NULL
		   AND EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'sessions'
			  AND column_name = 'origin'
		   )`).Scan(&schemaReady); err != nil {
		return fmt.Errorf("inspect sandbox workload routing prerequisites: %w", err)
	}
	if !schemaReady {
		return nil
	}

	statements := []struct {
		name string
		sql  string
	}{
		{name: "set lock timeout", sql: `SET lock_timeout = '5s'`},
		{name: "add routing columns", sql: `ALTER TABLE jobs
			ADD COLUMN IF NOT EXISTS workload_class text NOT NULL DEFAULT 'interactive',
			ADD COLUMN IF NOT EXISTS sandbox_slot_reserved_until timestamptz`},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.sql); err != nil {
			return fmt.Errorf("%s: %w", statement.name, err)
		}
	}
	// The short timeout protects only blocking table/row locks. Concurrent
	// index construction is intentionally allowed to wait for the brief locks it
	// needs instead of turning ordinary jobs-table traffic into a deploy failure.
	if _, err := conn.Exec(ctx, `SET lock_timeout = '0'`); err != nil {
		return fmt.Errorf("reset lock timeout before concurrent indexes: %w", err)
	}
	// Concurrent builds may legitimately outlive the normal request timeout,
	// but they still need a finite ceiling so a deploy cannot hang forever on a
	// stalled snapshot or storage failure.
	if _, err := conn.Exec(ctx, `SET statement_timeout = '30min'`); err != nil {
		return fmt.Errorf("set statement timeout before concurrent indexes: %w", err)
	}

	for _, index := range sandboxRoutingConcurrentIndexes {
		var invalid bool
		if err := conn.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_index i
				JOIN pg_class c ON c.oid = i.indexrelid
				WHERE c.relname = $1
				  AND NOT i.indisvalid
			)`, index.name).Scan(&invalid); err != nil {
			return fmt.Errorf("inspect concurrent index %s: %w", index.name, err)
		}
		if invalid {
			if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+index.name); err != nil {
				return fmt.Errorf("drop invalid concurrent index %s: %w", index.name, err)
			}
		}
		if _, err := conn.Exec(ctx, index.createSQL); err != nil {
			return fmt.Errorf("create concurrent index %s: %w", index.name, err)
		}
	}
	if _, err := conn.Exec(ctx, `SET statement_timeout = '0'`); err != nil {
		return fmt.Errorf("reset statement timeout after concurrent indexes: %w", err)
	}

	// Build the partial active-turn index before the one-time classification
	// repair so the backfill walks the small active sandbox-job set instead of
	// scanning the entire hot queue table.
	if _, err := conn.Exec(ctx, `SET lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("set lock timeout before sandbox workload backfill: %w", err)
	}
	if _, err := conn.Exec(ctx, sandboxWorkloadBackfillSQL); err != nil {
		return fmt.Errorf("backfill active code reviews: %w", err)
	}
	if _, err := conn.Exec(ctx, `SET lock_timeout = '0'`); err != nil {
		return fmt.Errorf("reset lock timeout after sandbox workload backfill: %w", err)
	}
	return nil
}

// repairSandboxWorkloadRoutingDirtyMigration makes direct `migrate up` calls
// recover the same way as the deploy wrapper. Migrations 286 and 287 are each
// executed by the Postgres driver as one transaction, so a failure rolls their
// schema/data work back while leaving only golang-migrate's dirty marker to
// rewind.
func repairSandboxWorkloadRoutingDirtyMigration(m dirtyMigrationRepairer) (bool, error) {
	version, dirty, err := m.Version()
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			return false, nil
		}
		return false, fmt.Errorf("read migration version: %w", err)
	}
	if !dirty || (version != sandboxWorkloadRoutingMigrationVersion && version != sandboxWorkloadRoutingValidationMigrationVersion) {
		return false, nil
	}
	previousVersion := int(version) - 1
	if err := m.Force(previousVersion); err != nil {
		return false, fmt.Errorf("force migration version to %d: %w", previousVersion, err)
	}
	return true, nil
}

// repairKnownDirtyMigration clears only dirty markers whose production failure
// was observed and whose migration transaction was verified to have rolled back
// completely. This is deliberately an exact allowlist rather than a general
// force command: an unknown dirty version may have out-of-transaction effects
// or require a data repair before it is safe to retry.
func repairKnownDirtyMigration(m dirtyMigrationRepairer) (uint, bool, error) {
	version, dirty, err := m.Version()
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read migration version: %w", err)
	}
	if !dirty {
		return 0, false, nil
	}

	previousVersion, known := knownDirtyMigrationPreviousVersion(version)
	if !known {
		return 0, false, fmt.Errorf(
			"database is dirty at version %d; refusing repair because only versions %d, %d, %d, and %d are allowlisted",
			version,
			prReadinessDirtyMigrationVersion,
			codeReviewDisputesDirtyMigrationVersion,
			sandboxWorkloadRoutingMigrationVersion,
			sandboxWorkloadRoutingValidationMigrationVersion,
		)
	}
	if err := m.Force(previousVersion); err != nil {
		return 0, false, fmt.Errorf("force migration version to %d: %w", previousVersion, err)
	}
	return version, true, nil
}

func knownDirtyMigrationPreviousVersion(version uint) (int, bool) {
	switch version {
	case prReadinessDirtyMigrationVersion,
		codeReviewDisputesDirtyMigrationVersion,
		sandboxWorkloadRoutingMigrationVersion,
		sandboxWorkloadRoutingValidationMigrationVersion:
		return int(version) - 1, true
	default:
		return 0, false
	}
}

// repairPRReadinessDirtyMigration repairs only the known production failure of
// the original migration 267. PostgreSQL executed that migration file as one
// transaction, so its deadlock rolled back every schema/data statement while
// golang-migrate's separately-written dirty marker remained at 267. Refuse to
// clear any other dirty version so this cannot become a general force bypass.
func repairPRReadinessDirtyMigration(m dirtyMigrationRepairer) (bool, error) {
	version, dirty, err := m.Version()
	if err != nil {
		if errors.Is(err, migrate.ErrNilVersion) {
			return false, nil
		}
		return false, fmt.Errorf("read migration version: %w", err)
	}
	if !dirty {
		return false, nil
	}
	if version != prReadinessDirtyMigrationVersion {
		return false, fmt.Errorf("database is dirty at version %d; refusing targeted repair for version %d", version, prReadinessDirtyMigrationVersion)
	}
	if err := m.Force(prReadinessDirtyMigrationVersion - 1); err != nil {
		return false, fmt.Errorf("force migration version to %d: %w", prReadinessDirtyMigrationVersion-1, err)
	}
	return true, nil
}

func resolveMigrationSource(exists func(string) bool) (string, error) {
	candidates := []struct {
		path   string
		source string
	}{
		{path: "migrations", source: "file://migrations"},
		{path: "/migrations", source: "file:///migrations"},
	}

	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		candidates = append(candidates, struct {
			path   string
			source string
		}{
			path:   filepath.Join(execDir, "migrations"),
			source: fmt.Sprintf("file://%s", filepath.Join(execDir, "migrations")),
		})
	}

	for _, candidate := range candidates {
		if exists(candidate.path) {
			return candidate.source, nil
		}
	}

	return "", fmt.Errorf("searched %q, %q, and executable-adjacent migrations", "migrations", "/migrations")
}

func pathExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// logMigrationError prints detailed diagnostics for a failed migration.
func logMigrationError(direction string, m *migrate.Migrate, err error) {
	fmt.Fprintln(os.Stderr, "========================================")
	fmt.Fprintf(os.Stderr, "MIGRATION %s FAILED\n", direction)
	fmt.Fprintln(os.Stderr, "========================================")

	// Print current version and dirty state.
	version, dirty, verr := m.Version()
	if verr == nil {
		fmt.Fprintf(os.Stderr, "  Version: %d\n", version)
		fmt.Fprintf(os.Stderr, "  Dirty:   %v\n", dirty)
	}

	// Check for ErrDirty — this means a *previous* run failed and left the DB
	// in a broken state. The actual root-cause error was logged on that run.
	var dirtyErr migrate.ErrDirty
	if errors.As(err, &dirtyErr) {
		fmt.Fprintf(os.Stderr, "\nDatabase is dirty at version %d.\n", dirtyErr.Version)
		fmt.Fprintln(os.Stderr, "A previous migration failed and left the database in an inconsistent state.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To fix:")
		fmt.Fprintf(os.Stderr, "  1. Check the migration file: migrations/%06d_*.up.sql\n", dirtyErr.Version)
		fmt.Fprintln(os.Stderr, "  2. Fix the data or migration, then clear the dirty flag:")
		fmt.Fprintf(os.Stderr, "     UPDATE schema_migrations SET version = %d, dirty = false;\n", dirtyErr.Version-1)
		fmt.Fprintln(os.Stderr, "  3. Re-run migrations.")
		fmt.Fprintln(os.Stderr, "========================================")
		return
	}

	// Check for database.Error — contains the SQL query excerpt and line number.
	var dbErr database.Error
	if errors.As(err, &dbErr) {
		fmt.Fprintln(os.Stderr, "\nDatabase error details:")
		if dbErr.Line > 0 {
			fmt.Fprintf(os.Stderr, "  Line:    %d\n", dbErr.Line)
		}
		if len(dbErr.Query) > 0 {
			fmt.Fprintf(os.Stderr, "  Query:   %s\n", dbErr.Query)
		}
		if dbErr.OrigErr != nil {
			fmt.Fprintf(os.Stderr, "  Cause:   %v\n", dbErr.OrigErr)
		}
		if dbErr.Err != "" {
			fmt.Fprintf(os.Stderr, "  Detail:  %s\n", dbErr.Err)
		}
	}

	// Always print the full error chain.
	fmt.Fprintf(os.Stderr, "\nFull error: %v\n", err)

	// Unwrap and print the full error chain for debugging.
	fmt.Fprintln(os.Stderr, "\nError chain:")
	for i, e := 1, errors.Unwrap(err); e != nil; i, e = i+1, errors.Unwrap(e) {
		fmt.Fprintf(os.Stderr, "  [%d] %T: %v\n", i, e, e)
	}

	fmt.Fprintln(os.Stderr, "========================================")
}
