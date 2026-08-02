package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

type fakeDirtyMigrationRepairer struct {
	version      uint
	dirty        bool
	versionErr   error
	forceErr     error
	forcedTo     int
	forceInvoked bool
}

func (f *fakeDirtyMigrationRepairer) Version() (uint, bool, error) {
	return f.version, f.dirty, f.versionErr
}

func (f *fakeDirtyMigrationRepairer) Force(version int) error {
	f.forceInvoked = true
	f.forcedTo = version
	return f.forceErr
}

func TestRepairPRReadinessDirtyMigration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		repairer         fakeDirtyMigrationRepairer
		expectedRepaired bool
		expectedForce    int
		expectForce      bool
		expectError      bool
	}{
		{
			name:             "repairs exact dirty readiness migration",
			repairer:         fakeDirtyMigrationRepairer{version: prReadinessDirtyMigrationVersion, dirty: true},
			expectedRepaired: true,
			expectedForce:    prReadinessDirtyMigrationVersion - 1,
			expectForce:      true,
		},
		{
			name:     "does nothing for clean database",
			repairer: fakeDirtyMigrationRepairer{version: prReadinessDirtyMigrationVersion, dirty: false},
		},
		{
			name:        "refuses unrelated dirty migration",
			repairer:    fakeDirtyMigrationRepairer{version: prReadinessDirtyMigrationVersion + 1, dirty: true},
			expectError: true,
		},
		{
			name:        "returns version read failure",
			repairer:    fakeDirtyMigrationRepairer{versionErr: errors.New("version unavailable")},
			expectError: true,
		},
		{
			name:     "does nothing before the first migration",
			repairer: fakeDirtyMigrationRepairer{versionErr: migrate.ErrNilVersion},
		},
		{
			name:          "returns force failure",
			repairer:      fakeDirtyMigrationRepairer{version: prReadinessDirtyMigrationVersion, dirty: true, forceErr: errors.New("force rejected")},
			expectForce:   true,
			expectedForce: prReadinessDirtyMigrationVersion - 1,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repairer := tt.repairer
			repaired, err := repairPRReadinessDirtyMigration(&repairer)
			if tt.expectError {
				require.Error(t, err, "targeted migration repair should return the expected failure")
			} else {
				require.NoError(t, err, "targeted migration repair should complete without error")
			}
			require.Equal(t, tt.expectedRepaired, repaired, "targeted migration repair should report whether it changed migration state")
			require.Equal(t, tt.expectForce, repairer.forceInvoked, "targeted migration repair should only force the exact known dirty version")
			if tt.expectForce {
				require.Equal(t, tt.expectedForce, repairer.forcedTo, "targeted migration repair should rewind to the version before readiness removal")
			}
		})
	}
}

func TestResolveMigrationSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		paths    map[string]bool
		expected string
	}{
		{
			name: "prefers repo relative migrations directory",
			paths: map[string]bool{
				"migrations":  true,
				"/migrations": true,
			},
			expected: "file://migrations",
		},
		{
			name: "falls back to container absolute migrations directory",
			paths: map[string]bool{
				"/migrations": true,
			},
			expected: "file:///migrations",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := resolveMigrationSource(func(path string) bool {
				return tt.paths[path]
			})

			require.NoError(t, err, "resolveMigrationSource should find an available migrations directory")
			require.Equal(t, tt.expected, actual, "resolveMigrationSource should return the expected source URL")
		})
	}
}

func TestResolveMigrationSourceReturnsErrorWhenNoDirectoryExists(t *testing.T) {
	t.Parallel()

	_, err := resolveMigrationSource(func(path string) bool {
		return false
	})

	require.Error(t, err, "resolveMigrationSource should fail when no migrations directory is available")
}

func TestMigrationVersionsAreUnique(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err, "should glob migration files without error")

	versionPattern := regexp.MustCompile(`^(\d{6})_.+\.(up|down)\.sql$`)
	seen := make(map[string]string, len(files))

	for _, path := range files {
		base := filepath.Base(path)
		matches := versionPattern.FindStringSubmatch(base)
		require.Len(t, matches, 3, "migration filename should include a 6-digit version and direction")

		key := matches[1] + "." + matches[2]

		if previous, ok := seen[key]; ok {
			require.Failf(
				t,
				"duplicate migration version-direction",
				"migration slot %s is used by both %s and %s",
				key,
				previous,
				base,
			)
		}
		seen[key] = base
	}
}

func TestMigrationsDoNotUseConcurrentIndexes(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	require.NoError(t, err, "should glob migration files without error")

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			contents, err := os.ReadFile(path)
			require.NoError(t, err, "migration file should be readable")
			sql := stripSQLLineComments(string(contents))
			require.NotContains(
				t,
				strings.ToUpper(sql),
				"CREATE INDEX CONCURRENTLY",
				"migration files run inside a transaction and must not create indexes concurrently",
			)
			require.NotContains(
				t,
				strings.ToUpper(sql),
				"DROP INDEX CONCURRENTLY",
				"migration files run inside a transaction and must not drop indexes concurrently",
			)
		})
	}
}

func TestRenumberedPreviewResourceMigrationIsReplaySafe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required string
	}{
		{name: "peak memory bytes column", required: "ADD COLUMN IF NOT EXISTS peak_memory_bytes"},
		{name: "peak memory sampled at column", required: "ADD COLUMN IF NOT EXISTS peak_memory_sampled_at"},
		{name: "peak memory phase column", required: "ADD COLUMN IF NOT EXISTS peak_memory_phase"},
		{name: "resource samples table", required: "CREATE TABLE IF NOT EXISTS preview_resource_samples"},
		{name: "preview samples index", required: "CREATE INDEX IF NOT EXISTS idx_preview_resource_samples_preview"},
		{name: "sampled at index", required: "CREATE INDEX IF NOT EXISTS idx_preview_resource_samples_sampled_at"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000249_preview_resource_samples.up.sql"))
			require.NoError(t, err, "renumbered preview resource migration should be readable")
			require.Contains(t, string(body), tt.required, "renumbered preview resource migration should tolerate schema applied under its former version")
		})
	}
}

// TestRenumberedDependencyCacheCostMigrationIsReplaySafe guards the same hazard
// as TestRenumberedPreviewResourceMigrationIsReplaySafe: this migration shipped
// as 000274 before that slot was claimed on main, so databases that applied the
// former version already hold these columns and constraints. Plain ADD COLUMN /
// ADD CONSTRAINT would abort the replay and leave schema_migrations dirty.
func TestRenumberedDependencyCacheCostMigrationIsReplaySafe(t *testing.T) {
	t.Parallel()

	columns := []string{
		"restore_attempt_count",
		"restore_success_count",
		"restore_total_duration_ms",
		"producer_duration_ms",
		"producer_benefit_count",
		"producer_benefit_total_ms",
		"last_restore_at",
	}
	constraints := []string{
		"preview_dependency_cache_restore_attempt_count_nonnegative",
		"preview_dependency_cache_restore_success_count_nonnegative",
		"preview_dependency_cache_restore_total_duration_nonnegative",
		"preview_dependency_cache_producer_duration_nonnegative",
		"preview_dependency_cache_producer_benefit_count_nonnegative",
		"preview_dependency_cache_producer_benefit_total_nonnegative",
		"preview_dependency_cache_restore_success_lte_attempts",
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000275_preview_dependency_cache_costs.up.sql"))
	require.NoError(t, err, "renumbered dependency cache cost migration should be readable")
	sql := string(body)

	for _, column := range columns {
		require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS "+column,
			"renumbered migration should tolerate %s already existing from its former version", column)
	}
	for _, constraint := range constraints {
		// Postgres has no ADD CONSTRAINT IF NOT EXISTS, so replay safety comes
		// from dropping the constraint before re-adding it.
		require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS "+constraint,
			"renumbered migration should drop %s before re-adding it", constraint)
		require.Contains(t, sql, "ADD CONSTRAINT "+constraint,
			"renumbered migration should re-add %s", constraint)
	}
}

func TestHotTableFKRemovalDownMigrationIsExplicitNoop(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000211_hot_table_fk_removal.down.sql"))
	require.NoError(t, err, "hot table FK removal down migration should be readable")

	sql := strings.ToUpper(stripSQLLineComments(string(body)))
	require.Contains(t, sql, "SELECT 1;", "down migration should execute an explicit no-op statement")
	require.NotContains(t, sql, "ADD CONSTRAINT", "down migration should not recreate reviewed hot-table FK exceptions")
}

func TestRemovePMMachineryMigrationIsRollingCompatible(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000260_remove_pm_machinery.up.sql"))
	require.NoError(t, err, "PM machinery migration should be readable")
	sql := strings.ToUpper(stripSQLLineComments(string(body)))

	required := []string{
		"CREATE VIEW SESSION_EXECUTION_CONTEXT",
		"CREATE VIEW REFERENCE_DOCUMENTS",
		"CREATE VIEW REFERENCE_CONTEXT_SET_PINS",
		"CREATE VIEW REFERENCE_CONTEXT_SET_PIN_MEMBERS",
		"ADD COLUMN REFERENCE_CONTEXT_SET_PIN_ID",
		"CREATE FUNCTION SYNC_REFERENCE_CONTEXT_SET_PIN_COLUMNS",
		"TRG_EVAL_TASKS_SYNC_REFERENCE_CONTEXT_SET_PIN",
		"TRG_EVAL_RUNS_SYNC_REFERENCE_CONTEXT_SET_PIN",
	}
	for _, fragment := range required {
		require.Contains(t, sql, fragment, "migration should expose the neutral schema while legacy binaries remain active")
	}

	forbidden := []string{
		"ALTER TABLE SESSION_PM_CONTEXT RENAME",
		"ALTER TABLE PM_DOCUMENTS RENAME",
		"ALTER TABLE PM_DOCUMENT_SET_PINS RENAME",
		"ALTER TABLE EVAL_TASKS RENAME COLUMN",
		"ALTER TABLE EVAL_RUNS RENAME COLUMN",
		"DROP TABLE PROJECT_SOURCE_ISSUES",
		"DROP COLUMN PROPOSED_BY_PM",
		"DROP COLUMN ELIGIBLE_FOR_AGENT",
		"DROP TRIGGER IF EXISTS TRG_REJECT_DISABLED_PM_JOBS",
	}
	for _, fragment := range forbidden {
		require.NotContains(t, sql, fragment, "expand migration should not invalidate old API or worker binaries")
	}
}

func stripSQLLineComments(contents string) string {
	lines := strings.Split(contents, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
