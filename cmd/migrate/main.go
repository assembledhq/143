package main

import (
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
		fmt.Println("Usage: migrate [up|down]")
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
