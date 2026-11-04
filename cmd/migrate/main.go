package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/version"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrateLogger implements migrate.Logger to surface verbose migration output.
type migrateLogger struct{ verbose bool }

const (
	prReadinessDirtyMigrationVersion        = 267
	codeReviewDisputesDirtyMigrationVersion = 281
	// Bump only after reviewing compatibility with the inactive runtime.
	// TestRuntimeRevertSchemaPinMatchesMigrations catches a newer source schema.
	inactiveRuntimeSchemaVersion = 397
)

type dirtyMigrationRepairer interface {
	Version() (uint, bool, error)
	Force(version int) error
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
		fmt.Println("Usage: migrate [up|down|repair-known-dirty|repair-pr-readiness|verify-inactive-rollback]")
		os.Exit(1)
	}
	// This read-only check must not initialize golang-migrate: opening its
	// database driver can create the migration ledger. It never runs old-image SQL.
	if os.Args[1] == "verify-inactive-rollback" {
		if len(os.Args) != 3 || (os.Args[2] != "392" && os.Args[2] != "393") {
			fmt.Fprintln(os.Stderr, "inactive binary rollback supports only explicitly pinned schema 392 or 393")
			os.Exit(1)
		}
		schema := uint(392)
		if os.Args[2] == "393" {
			schema = 393
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := verifyInactiveBinaryRollback(ctx, dbURL, schema); err != nil {
			fmt.Fprintf(os.Stderr, "Inactive binary rollback refused: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Inactive binary rollback preflight passed (schema %d)\n", schema)
		return
	}

	migrationSource, err := resolveMigrationSource(pathExists)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve migrations directory: %v\n", err)
		os.Exit(1)
	}

	dbURL = lifecycleMigrationDatabaseURL(dbURL, version.BuildSHA)
	m, err := migrate.New(migrationSource, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create migrator: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	m.Log = migrateLogger{verbose: os.Getenv("LOG_LEVEL") == "debug"}

	switch os.Args[1] {
	case "up":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := verifyRuntimeRevertBeforeUp(ctx, m, dbURL); err != nil {
			fmt.Fprintf(os.Stderr, "Inactive runtime preflight refused: %v\n", err)
			os.Exit(1)
		}
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			logMigrationError("up", m, err)
			os.Exit(1)
		}
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer verifyCancel()
		if err := verifyInactiveRuntimeSchema(verifyCtx, dbURL, inactiveRuntimeSchemaVersion); err != nil {
			fmt.Fprintf(os.Stderr, "Inactive runtime post-migration check refused: %v\n", err)
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

// The source revert keeps the migration history but removes participating
// writers. Refuse active or retained protocol state before upgrading an existing
// full lifecycle schema. Fresh/older installations are checked after migration,
// before the deployment can start the application. Activation must remain
// excluded throughout deployment; this read-only snapshot is not a global lock.
// Keep this helper in main.go: Make and CI invoke that file directly.
func verifyRuntimeRevertBeforeUp(ctx context.Context, ledger interface{ Version() (uint, bool, error) }, rawURL string) error {
	version, dirty, err := ledger.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read migration ledger: %w", err)
	}
	if dirty {
		return errors.New("dirty migration ledger")
	}
	if version > inactiveRuntimeSchemaVersion {
		return fmt.Errorf("inactive runtime supports schema through %d only", inactiveRuntimeSchemaVersion)
	}
	if version < 392 {
		return nil
	}
	return verifyInactiveRuntimeSchema(ctx, rawURL, version)
}

// verifyInactiveBinaryRollback is deliberately limited to the unactivated 0a
// schema and its additive package-credential table. The caller must pin the
// exact retained schema; future schemas require a new compatibility review.
// Operators must exclude activation and other deployments throughout rollback;
// this bounded read-only snapshot is not a fleet-wide maintenance lock.
func verifyInactiveBinaryRollback(ctx context.Context, rawURL string, schema uint) error {
	if schema != 392 && schema != 393 {
		return errors.New("inactive binary rollback supports only explicitly pinned schema 392 or 393")
	}
	return verifyInactiveRuntimeSchema(ctx, rawURL, schema)
}

// verifyInactiveRuntimeSchema shares the retained-history proof with the pinned
// binary rollback check. The source revert also supports later schemas through
// inactiveRuntimeSchemaVersion. The package-credential table, review phase
// constraint, size-limit override flag, automation comment-update support, and
// review-history receipt roles leave the inspected caller and lifecycle state intact.
func verifyInactiveRuntimeSchema(ctx context.Context, rawURL string, expectedSchema uint) (result error) {
	// Worker DATABASE_URL values include pool options. Parse them as a pool
	// configuration so they are not sent to PostgreSQL as runtime parameters;
	// the inspector still opens only one direct connection, never a pool.
	poolConfig, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		// Parse errors can echo the original DSN, including credentials.
		return errors.New("invalid rollback database connection settings")
	}
	cfg := poolConfig.ConnConfig
	// golang-migrate consumes x-* options itself, and lib/pq consumes its
	// fallback name locally. Neither is a PostgreSQL server setting. Preserve
	// the actual database target and all other options for the inspector.
	for key := range cfg.RuntimeParams {
		if strings.HasPrefix(key, "x-") || key == "fallback_application_name" {
			delete(cfg.RuntimeParams, key)
		}
	}
	cfg.RuntimeParams["application_name"] = "143:inactive-rollback-preflight"
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "30000"
	cfg.RuntimeParams["lock_timeout"] = "5000"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect rollback inspector: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result = errors.Join(result, conn.Close(closeCtx))
	}()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var refusal string
	err = tx.QueryRow(ctx, `SELECT CASE
 WHEN current_setting('transaction_read_only') <> 'on' THEN 'inspection is not read-only'
 WHEN (SELECT count(*) FROM public.schema_migrations) <> 1
   OR NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version=$1 AND NOT dirty)
   THEN $2
 WHEN (SELECT count(*) FROM public.caller_send_intent_control) <> 1
   OR NOT EXISTS (SELECT 1 FROM public.caller_send_intent_control
     WHERE singleton AND schema_ready AND dispatch_status_ready AND NOT intents_enabled)
   THEN 'caller participation is enabled or readiness is incomplete'
 WHEN EXISTS (SELECT 1 FROM public.sessions WHERE lifecycle_version <> 0)
   OR EXISTS (SELECT 1 FROM public.session_threads WHERE lifecycle_version <> 0)
   OR EXISTS (SELECT 1 FROM public.session_lifecycle_cutovers)
   OR EXISTS (SELECT 1 FROM public.session_input_dispatches)
   THEN 'lifecycle activation or retained dispatch history exists'
 WHEN EXISTS (SELECT 1 FROM public.caller_send_command_stamps)
   OR EXISTS (SELECT 1 FROM public.review_loop_start_intents)
   OR EXISTS (SELECT 1 FROM public.code_review_agent_results WHERE dispatch_version IS NOT NULL)
   OR EXISTS (SELECT 1 FROM public.session_review_loops WHERE startup_send_version IS NOT NULL)
   OR EXISTS (SELECT 1 FROM public.code_review_session_metadata WHERE status_code='code_reviewer_dispatch_unresolved')
   THEN 'retained caller history exists'
 ELSE '' END`, expectedSchema, fmt.Sprintf("expected clean migration %d", expectedSchema)).Scan(&refusal)
	if err != nil {
		return fmt.Errorf("inspect inactive rollback state: %w", err)
	}
	if refusal != "" {
		return errors.New(refusal)
	}
	return tx.Commit(ctx)
}

// lifecycleMigrationDatabaseURL overrides only passive connection attribution.
// Keep golang-migrate's original driver selection and URL.Query semantics: its
// postgres driver already drops malformed query pairs through FilterCustomQuery
// while preserving valid, repeated and x-* options. This is not validation of a
// database target, a participating writer registration or a compatibility gate.
func lifecycleMigrationDatabaseURL(raw, build string) string {
	return db.LifecycleMigrationDatabaseURL(raw, build)
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
			"database is dirty at version %d; refusing repair because only versions %d and %d are allowlisted",
			version,
			prReadinessDirtyMigrationVersion,
			codeReviewDisputesDirtyMigrationVersion,
		)
	}
	if err := m.Force(previousVersion); err != nil {
		return 0, false, fmt.Errorf("force migration version to %d: %w", previousVersion, err)
	}
	return version, true, nil
}

func knownDirtyMigrationPreviousVersion(version uint) (int, bool) {
	switch version {
	case prReadinessDirtyMigrationVersion, codeReviewDisputesDirtyMigrationVersion:
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
