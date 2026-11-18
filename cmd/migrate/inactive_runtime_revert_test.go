package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
)

func TestRuntimeRevertSchemaPinMatchesMigrations(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	require.NoError(t, err, "read the migration source used by deployments")
	require.NotEmpty(t, files, "the schema compatibility pin requires a nonempty migration source")
	var highest uint64
	for _, file := range files {
		prefix, _, ok := strings.Cut(filepath.Base(file), "_")
		require.True(t, ok, "each migration must have a numeric version prefix")
		version, err := strconv.ParseUint(prefix, 10, 64)
		require.NoError(t, err, "parse the source migration version")
		highest = max(highest, version)
	}
	require.Equal(t, uint64(inactiveRuntimeSchemaVersion), highest, "new migrations require a compatibility review and an explicit update of the inactive runtime schema pin")
}

func TestRuntimeRevertSingleFileEntrypoint(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// Make and CI invoke this single file. Package-only compilation would miss
	// references to helpers that are accidentally moved into sibling files.
	command := exec.CommandContext(ctx, "go", "run", "main.go", "verify-inactive-rollback", "394")
	output, err := command.CombinedOutput()
	require.Error(t, err, "the pinned binary rollback command must reject unreviewed schema 394 without connecting")
	require.NoError(t, ctx.Err(), "the entry point must compile and return within the test budget")
	require.Equal(t, "inactive binary rollback supports only explicitly pinned schema 392 or 393\nexit status 1\n", string(output), "the single-file entry point must reach its argument guard rather than fail compilation")
}

func TestRuntimeRevertMigrationPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ledger  fakeDirtyMigrationRepairer
		refusal string
	}{
		{name: "fresh database", ledger: fakeDirtyMigrationRepairer{versionErr: migrate.ErrNilVersion}},
		{name: "older schema checked after upgrade", ledger: fakeDirtyMigrationRepairer{version: 345}},
		{name: "partial clean schema checked after upgrade", ledger: fakeDirtyMigrationRepairer{version: 391}},
		{name: "dirty schema rejected before writes", ledger: fakeDirtyMigrationRepairer{version: 392, dirty: true}, refusal: "dirty migration ledger"},
		{name: "unknown newer schema", ledger: fakeDirtyMigrationRepairer{version: 399}, refusal: "schema through 398 only"},
		{name: "ledger error", ledger: fakeDirtyMigrationRepairer{versionErr: errors.New("synthetic read failure")}, refusal: "synthetic read failure"},
		{name: "392 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 392}, refusal: "invalid rollback database connection settings"},
		{name: "393 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 393}, refusal: "invalid rollback database connection settings"},
		{name: "394 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 394}, refusal: "invalid rollback database connection settings"},
		{name: "395 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 395}, refusal: "invalid rollback database connection settings"},
		{name: "396 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 396}, refusal: "invalid rollback database connection settings"},
		{name: "397 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 397}, refusal: "invalid rollback database connection settings"},
		{name: "398 requires real inspection", ledger: fakeDirtyMigrationRepairer{version: 398}, refusal: "invalid rollback database connection settings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := verifyRuntimeRevertBeforeUp(context.Background(), &tt.ledger, "postgres://invalid:%zz@localhost/test")
			if tt.refusal == "" {
				require.NoError(t, err, "only fresh or older clean schema may defer full inspection until after upgrade")
			} else {
				require.ErrorContains(t, err, tt.refusal, "unsupported or unreadable state must fail before migration")
			}
			require.False(t, tt.ledger.forceInvoked, "compatibility checks must never repair the migration ledger")
		})
	}
}
