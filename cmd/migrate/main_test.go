package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/pashagolub/pgxmock/v4"
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

func TestPrepareSandboxWorkloadRoutingOnConn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		schemaReady    bool
		invalidIndexes map[string]bool
	}{
		{
			name: "skips databases before the prerequisite schema exists",
		},
		{
			name:        "adds columns backfills reviews and creates indexes concurrently",
			schemaReady: true,
		},
		{
			name:        "rebuilds an invalid interrupted concurrent index",
			schemaReady: true,
			invalidIndexes: map[string]bool{
				"idx_jobs_sandbox_routing": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create migration preparation mock")
			defer mock.Close()

			mock.ExpectQuery(`(?s)SELECT to_regclass.*information_schema\.columns`).
				WillReturnRows(pgxmock.NewRows([]string{"schema_ready"}).AddRow(tt.schemaReady))
			if tt.schemaReady {
				mock.ExpectExec(`SET lock_timeout = '5s'`).
					WillReturnResult(pgxmock.NewResult("SET", 0))
				mock.ExpectExec(`(?s)ALTER TABLE jobs.*ADD COLUMN IF NOT EXISTS workload_class.*sandbox_slot_reserved_until`).
					WillReturnResult(pgxmock.NewResult("ALTER TABLE", 0))
				mock.ExpectExec(`(?s)UPDATE jobs j.*FROM sessions s.*s\.origin = 'code_review'.*j\.status IN \('pending', 'running'\)`).
					WillReturnResult(pgxmock.NewResult("UPDATE", 2))
				mock.ExpectExec(`SET lock_timeout = '0'`).
					WillReturnResult(pgxmock.NewResult("SET", 0))
				for _, index := range sandboxRoutingConcurrentIndexes {
					invalid := tt.invalidIndexes[index.name]
					mock.ExpectQuery(`(?s)SELECT EXISTS.*pg_index.*NOT i\.indisvalid`).
						WithArgs(index.name).
						WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(invalid))
					if invalid {
						mock.ExpectExec("DROP INDEX CONCURRENTLY IF EXISTS " + index.name).
							WillReturnResult(pgxmock.NewResult("DROP INDEX", 0))
					}
					mock.ExpectExec(`(?s)CREATE INDEX CONCURRENTLY IF NOT EXISTS ` + index.name).
						WillReturnResult(pgxmock.NewResult("CREATE INDEX", 0))
				}
			}

			err = prepareSandboxWorkloadRoutingOnConn(context.Background(), mock)
			require.NoError(t, err, "sandbox workload routing preparation should complete")
			require.NoError(t, mock.ExpectationsWereMet(), "preparation should execute the staged hot-table rollout in order")
		})
	}
}

func TestRepairSandboxWorkloadRoutingDirtyMigration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		repairer      fakeDirtyMigrationRepairer
		expectRepair  bool
		expectForce   bool
		expectedForce int
		expectError   bool
	}{
		{
			name:          "rewinds dirty routing migration",
			repairer:      fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion, dirty: true},
			expectRepair:  true,
			expectForce:   true,
			expectedForce: sandboxWorkloadRoutingMigrationVersion - 1,
		},
		{
			name:     "leaves clean routing migration unchanged",
			repairer: fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion},
		},
		{
			name:     "leaves unrelated dirty migration for normal diagnostics",
			repairer: fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion - 1, dirty: true},
		},
		{
			name:     "allows an empty database",
			repairer: fakeDirtyMigrationRepairer{versionErr: migrate.ErrNilVersion},
		},
		{
			name:        "returns version inspection failure",
			repairer:    fakeDirtyMigrationRepairer{versionErr: errors.New("version unavailable")},
			expectError: true,
		},
		{
			name:          "returns force failure",
			repairer:      fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion, dirty: true, forceErr: errors.New("force rejected")},
			expectForce:   true,
			expectedForce: sandboxWorkloadRoutingMigrationVersion - 1,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repairer := tt.repairer
			repaired, err := repairSandboxWorkloadRoutingDirtyMigration(&repairer)
			if tt.expectError {
				require.Error(t, err, "routing migration repair should return the expected failure")
			} else {
				require.NoError(t, err, "routing migration repair should complete without error")
			}
			require.Equal(t, tt.expectRepair, repaired, "routing migration repair should report whether it rewound version 286")
			require.Equal(t, tt.expectForce, repairer.forceInvoked, "routing migration repair should force only dirty version 286")
			if tt.expectForce {
				require.Equal(t, tt.expectedForce, repairer.forcedTo, "routing migration repair should rewind to version 285")
			}
		})
	}
}

func TestSandboxWorkloadRoutingPreparationRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reader    fakeDirtyMigrationRepairer
		expected  bool
		expectErr bool
	}{
		{
			name:     "prepares a fresh database",
			reader:   fakeDirtyMigrationRepairer{versionErr: migrate.ErrNilVersion},
			expected: true,
		},
		{
			name:     "prepares before routing migration",
			reader:   fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion - 1},
			expected: true,
		},
		{
			name:     "allows recovery from dirty routing migration",
			reader:   fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion, dirty: true},
			expected: true,
		},
		{
			name:   "skips after routing migration is applied",
			reader: fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion},
		},
		{
			name:   "skips later migration versions",
			reader: fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion + 1},
		},
		{
			name:      "returns migration state failures",
			reader:    fakeDirtyMigrationRepairer{versionErr: errors.New("version unavailable")},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			required, err := sandboxWorkloadRoutingPreparationRequired(&tt.reader)
			if tt.expectErr {
				require.Error(t, err, "migration state inspection should return the expected failure")
			} else {
				require.NoError(t, err, "migration state inspection should complete")
			}
			require.Equal(t, tt.expected, required, "preparation should only run until migration 286 is safely applied")
		})
	}
}

func TestRepairKnownDirtyMigration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		repairer         fakeDirtyMigrationRepairer
		expectedVersion  uint
		expectedRepaired bool
		expectedForce    int
		expectForce      bool
		expectError      bool
	}{
		{
			name:             "repairs exact dirty readiness migration",
			repairer:         fakeDirtyMigrationRepairer{version: prReadinessDirtyMigrationVersion, dirty: true},
			expectedVersion:  prReadinessDirtyMigrationVersion,
			expectedRepaired: true,
			expectedForce:    prReadinessDirtyMigrationVersion - 1,
			expectForce:      true,
		},
		{
			name:             "repairs exact dirty code review disputes migration",
			repairer:         fakeDirtyMigrationRepairer{version: codeReviewDisputesDirtyMigrationVersion, dirty: true},
			expectedVersion:  codeReviewDisputesDirtyMigrationVersion,
			expectedRepaired: true,
			expectedForce:    codeReviewDisputesDirtyMigrationVersion - 1,
			expectForce:      true,
		},
		{
			name:             "repairs exact dirty sandbox workload routing migration",
			repairer:         fakeDirtyMigrationRepairer{version: sandboxWorkloadRoutingMigrationVersion, dirty: true},
			expectedVersion:  sandboxWorkloadRoutingMigrationVersion,
			expectedRepaired: true,
			expectedForce:    sandboxWorkloadRoutingMigrationVersion - 1,
			expectForce:      true,
		},
		{
			name:     "does nothing for clean database",
			repairer: fakeDirtyMigrationRepairer{version: codeReviewDisputesDirtyMigrationVersion, dirty: false},
		},
		{
			name:        "refuses unrelated dirty migration",
			repairer:    fakeDirtyMigrationRepairer{version: codeReviewDisputesDirtyMigrationVersion + 1, dirty: true},
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
			repairer:      fakeDirtyMigrationRepairer{version: codeReviewDisputesDirtyMigrationVersion, dirty: true, forceErr: errors.New("force rejected")},
			expectForce:   true,
			expectedForce: codeReviewDisputesDirtyMigrationVersion - 1,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repairer := tt.repairer
			version, repaired, err := repairKnownDirtyMigration(&repairer)
			if tt.expectError {
				require.Error(t, err, "known migration repair should return the expected failure")
			} else {
				require.NoError(t, err, "known migration repair should complete without error")
			}
			require.Equal(t, tt.expectedVersion, version, "known migration repair should report the repaired version")
			require.Equal(t, tt.expectedRepaired, repaired, "known migration repair should report whether it changed migration state")
			require.Equal(t, tt.expectForce, repairer.forceInvoked, "known migration repair should only force an exact allowlisted dirty version")
			if tt.expectForce {
				require.Equal(t, tt.expectedForce, repairer.forcedTo, "known migration repair should rewind to the version before the failed transaction")
			}
		})
	}
}

func TestRepairPRReadinessDirtyMigrationRemainsNarrow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     uint
		expectForce bool
		expectError bool
	}{
		{
			name:        "repairs readiness version",
			version:     prReadinessDirtyMigrationVersion,
			expectForce: true,
		},
		{
			name:        "refuses code review disputes version",
			version:     codeReviewDisputesDirtyMigrationVersion,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repairer := fakeDirtyMigrationRepairer{version: tt.version, dirty: true}
			repaired, err := repairPRReadinessDirtyMigration(&repairer)
			if tt.expectError {
				require.Error(t, err, "legacy readiness repair should refuse a different dirty version")
			} else {
				require.NoError(t, err, "legacy readiness repair should repair its exact dirty version")
			}
			require.Equal(t, tt.expectForce, repairer.forceInvoked, "legacy readiness repair should remain scoped to version 267")
			require.Equal(t, tt.expectForce, repaired, "legacy readiness repair should report only its own repair")
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

func TestSandboxWorkloadRoutingMigrationIsStagedForHotTable(t *testing.T) {
	t.Parallel()

	shapeBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000286_sandbox_workload_routing.up.sql"))
	require.NoError(t, err, "sandbox routing shape migration should be readable")
	shapeSQL := string(shapeBody)
	lockTimeoutAt := strings.Index(shapeSQL, "SET LOCAL lock_timeout")
	alterJobsAt := strings.Index(shapeSQL, "ALTER TABLE jobs")
	require.GreaterOrEqual(t, lockTimeoutAt, 0, "shape migration should install a lock timeout")
	require.GreaterOrEqual(t, alterJobsAt, 0, "shape migration should alter the jobs table")
	require.Less(t, lockTimeoutAt, alterJobsAt, "lock timeout should be installed before the first jobs-table DDL")
	require.Contains(t, shapeSQL, "ADD COLUMN IF NOT EXISTS workload_class", "shape migration should tolerate columns preinstalled by migrate up")
	require.Contains(t, shapeSQL, "s.origin = 'code_review'", "shape migration should preserve active code-review classification")
	require.NotContains(t, shapeSQL, "VALIDATE CONSTRAINT chk_jobs_workload_class", "shape migration should not retain its table lock while validating existing rows")

	validationBody, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000287_validate_sandbox_workload_routing.up.sql"))
	require.NoError(t, err, "sandbox routing validation migration should be readable")
	validationSQL := string(validationBody)
	require.Contains(t, validationSQL, "SET LOCAL lock_timeout", "validation should have a bounded lock acquisition")
	require.Contains(t, validationSQL, "VALIDATE CONSTRAINT chk_jobs_workload_class", "validation should run in its own migration transaction")
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

func TestRenumberedDependencyCacheCostMigrationRepairsDisplacedMainMigration(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000277_repair_code_review_pull_request_history_index.up.sql"))
	require.NoError(t, err, "displaced migration repair should be readable")
	require.Contains(t, string(body), "CREATE INDEX IF NOT EXISTS idx_code_review_metadata_pull_request_created", "repair should install the main migration skipped by databases that ran the former branch version 274")
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
