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
)

// migrateLogger implements migrate.Logger to surface verbose migration output.
type migrateLogger struct{ verbose bool }

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
		fmt.Println("Usage: migrate [up|down|verify]")
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

	migrationDir := migrationDirFromSource(migrationSource)

	switch os.Args[1] {
	case "up":
		// Destructive-migration gate: pending destructive migrations may not
		// run until the deployed stable release satisfies their annotated
		// floor. Enforced only when the deploy pipeline provides
		// STABLE_MAX_MIGRATION; a no-op for local dev and CI.
		dbVersion, dirty, verr := m.Version()
		if verr != nil && verr != migrate.ErrNilVersion {
			logMigrationError("up", m, verr)
			os.Exit(1)
		}
		if dirty {
			logMigrationError("up", m, migrate.ErrDirty{Version: int(dbVersion)}) // #nosec G115 -- schema versions are far below int max
			os.Exit(1)
		}
		if err := runDestructiveGate(migrationDir, uint64(dbVersion)); err != nil {
			fmt.Fprintf(os.Stderr, "Destructive migration gate refused the run: %v\n", err)
			os.Exit(1)
		}
		// A database ahead of this checkout means this binary belongs to the
		// stable (pinned) plane and the canary pipeline owns the schema.
		// golang-migrate's behavior with a source set older than the DB
		// version is version-dependent, so never invoke it: degrade to the
		// stable preflight (schema + destructive-floor assertions) instead.
		// This makes a manual `deploy.sh app ...` against a pinned fleet
		// safe even without APP_SCHEMA_MODE=verify.
		if localMax, lmErr := maxLocalMigrationVersion(migrationDir); lmErr == nil && verr != migrate.ErrNilVersion && uint64(dbVersion) > localMax {
			fmt.Fprintf(os.Stderr, "WARNING: database schema (%d) is ahead of this checkout's migration set (%06d); running verify instead of up.\n", dbVersion, localMax)
			if err := runVerify(context.Background(), dbURL, migrationDir); err != nil {
				fmt.Fprintf(os.Stderr, "Schema verify FAILED: %v\n", err)
				os.Exit(1)
			}
			return
		}
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			logMigrationError("up", m, err)
			os.Exit(1)
		}
		// Persist the floors of every applied destructive migration so stable
		// preflights can enforce them without this checkout's files.
		appliedThrough, _, verr := m.Version()
		if verr != nil && verr != migrate.ErrNilVersion {
			fmt.Fprintf(os.Stderr, "Failed to read applied version for floor recording: %v\n", verr)
			os.Exit(1)
		}
		if err := recordFloorsAfterUp(context.Background(), dbURL, migrationDir, uint64(appliedThrough)); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to record destructive-compatibility floors: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Migrations applied successfully.")
	case "down":
		if err := m.Down(); err != nil && err != migrate.ErrNoChange {
			logMigrationError("down", m, err)
			os.Exit(1)
		}
		fmt.Println("Migrations rolled back successfully.")
	case "verify":
		// Stable-plane preflight: the schema (owned by the canary pipeline)
		// must already be at least as new as this checkout expects, and this
		// checkout must satisfy every recorded destructive floor. Never
		// migrates.
		if err := runVerify(context.Background(), dbURL, migrationDir); err != nil {
			fmt.Fprintf(os.Stderr, "Schema verify FAILED: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1]) // #nosec G705 -- writing to stderr, not HTTP response
		os.Exit(1)
	}
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
