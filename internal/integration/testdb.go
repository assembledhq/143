//go:build integration

// Package integration contains end-to-end tests that exercise the real
// production code paths against a real Postgres database. They guard the
// critical session lifecycle flows (push, create, retry, end) and the worker
// dispatch path against refactor regressions that pgxmock-based unit tests
// cannot catch (SQL typos, schema drift, transaction-boundary bugs, queue
// pickup wiring).
//
// Tests are gated behind the `integration` build tag and require
// INTEGRATION_DATABASE_URL pointing at a writable test database. The CI
// `backend-test` job already provisions a Postgres 17 service container; this
// suite reuses it via `make test-integration`.
package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assembledhq/143/internal/db"
)

// integrationDatabaseURLEnv names the env var that points the integration test
// suite at a writable Postgres. Distinct from DATABASE_URL so a developer who
// runs `make test` against their dev DB doesn't accidentally have integration
// tests truncate their dev data — they must opt in explicitly via the
// integration target.
const integrationDatabaseURLEnv = "INTEGRATION_DATABASE_URL"

var (
	poolOnce   sync.Once
	poolErr    error
	sharedPool *pgxpool.Pool
)

// requirePool returns a process-wide pgxpool.Pool against the integration
// database, having run migrations exactly once. Tests should call this from
// the top of each test function — it skips (rather than fails) when the env
// var is unset so the suite is a no-op outside CI / `make test-integration`.
func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dbURL := os.Getenv(integrationDatabaseURLEnv)
	if dbURL == "" {
		t.Skipf("set %s to run integration tests (e.g. via `make test-integration`)", integrationDatabaseURLEnv)
	}

	poolOnce.Do(func() {
		if err := runMigrations(dbURL); err != nil {
			poolErr = err
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		_ = cancel // pool outlives the test binary; we deliberately don't cancel
		pool, err := db.NewPool(ctx, dbURL)
		if err != nil {
			poolErr = err
			return
		}
		sharedPool = pool
	})

	if poolErr != nil {
		t.Fatalf("integration test pool unavailable: %v", poolErr)
	}
	return sharedPool
}

// runMigrations applies all up migrations against the test database. Run
// once per process. ErrNoChange is treated as success — a freshly migrated DB
// from a previous run is the common path.
func runMigrations(dbURL string) error {
	source, err := resolveMigrationSource()
	if err != nil {
		return err
	}
	m, err := migrate.New(source, dbURL)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// resolveMigrationSource finds the migrations directory by walking up from
// the current working directory until it locates a `migrations/` folder.
// Falls back to the binary-adjacent path for `go test -c` workflows. Mirrors
// the lookup logic in cmd/migrate/main.go so the two stay in sync.
func resolveMigrationSource() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "migrations")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return "file://" + candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("could not locate migrations/ directory by walking up from " + cwd)
}

// truncatedTables lists every multi-tenant or test-relevant table that needs
// resetting between integration tests. Truncating with CASCADE handles the
// foreign-key web (sessions → session_messages, session_logs, etc.) without
// us tracking dependency order by hand.
//
// schema_migrations is intentionally excluded — re-running migrations between
// tests would dominate the wall-clock budget.
var truncatedTables = []string{
	"nodes",
	"jobs",
	"session_messages",
	"session_logs",
	"session_questions",
	"session_threads",
	"session_issue_links",
	"session_turn_issue_snapshots",
	"sessions",
	"issues",
	"repositories",
	"integrations",
	"auth_sessions",
	"organization_memberships",
	"users",
	"organizations",
}

// resetDB truncates every test-relevant table so each test starts from a
// clean slate. Called via t.Cleanup at the *start* of each test (so the next
// test inherits a clean DB regardless of whether the prior one panicked).
//
// CASCADE traverses FK relationships — pgcrypto/uuid extensions and
// schema_migrations are untouched. Wrapped in a single statement because
// TRUNCATE acquires AccessExclusiveLock per table; one statement minimizes
// the lock surface and is atomic in case the test process crashes mid-truncate.
func resetDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	stmt := "TRUNCATE TABLE "
	for i, table := range truncatedTables {
		if i > 0 {
			stmt += ", "
		}
		stmt += table
	}
	stmt += " RESTART IDENTITY CASCADE"
	if _, err := pool.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("reset integration DB: %v", err)
	}
}

// setup pairs requirePool + resetDB. Most tests want both: a live pool and a
// clean slate. The Cleanup hook fires at end-of-test too so a crash mid-suite
// doesn't leak data into the next file's tests.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := requirePool(t)
	resetDB(t, pool)
	t.Cleanup(func() { resetDB(t, pool) })
	return pool
}
