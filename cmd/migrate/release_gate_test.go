package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeMigration(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
}

func TestMaxLocalMigrationVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeMigration(t, dir, "000001_init.up.sql", "CREATE TABLE a (id int);")
	writeMigration(t, dir, "000001_init.down.sql", "DROP TABLE a;")
	writeMigration(t, dir, "000245_release_channels.up.sql", "SELECT 1;")
	writeMigration(t, dir, "000012_other.up.sql", "SELECT 1;")

	maxVersion, err := maxLocalMigrationVersion(dir)
	require.NoError(t, err, "maxLocalMigrationVersion should parse the directory")
	require.Equal(t, uint64(245), maxVersion, "should return the highest up-migration number")
}

func TestMaxLocalMigrationVersion_EmptyDirErrors(t *testing.T) {
	t.Parallel()

	_, err := maxLocalMigrationVersion(t.TempDir())
	require.Error(t, err, "a directory with no migrations should error rather than report version 0")
}

func TestParseDestructiveFloors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeMigration(t, dir, "000250_drop_legacy.up.sql",
		"-- lint:destructive-ok-after schema=\"000240\" reason=\"stable >= 000240 no longer reads issues.legacy_state\"\nALTER TABLE issues DROP COLUMN legacy_state;\n")
	writeMigration(t, dir, "000251_additive.up.sql", "ALTER TABLE issues ADD COLUMN note text;")
	writeMigration(t, dir, "000252_rename.up.sql",
		"ALTER TABLE foo ADD COLUMN bar text;\n-- lint:destructive-ok-after schema=\"000251\" reason=\"stable stopped writing foo.old\"\nALTER TABLE foo DROP COLUMN old;\n")

	floors, err := parseDestructiveFloors(dir)
	require.NoError(t, err, "parseDestructiveFloors should parse annotations")
	require.Equal(t, map[uint64]uint64{250: 240, 252: 251}, floors,
		"should map annotated migration versions to their floors and skip unannotated files")
}

func TestCheckDestructiveGate(t *testing.T) {
	t.Parallel()

	floors := map[uint64]uint64{
		250: 240, // pending, floor satisfied when stable >= 240
		252: 251, // pending, floor NOT satisfied when stable < 251
		200: 190, // already applied — never blocks
	}

	t.Run("blocks pending destructive migrations above the stable floor", func(t *testing.T) {
		t.Parallel()
		blocked := checkDestructiveGate(floors, 245, 240)
		require.Equal(t, []uint64{252}, blocked,
			"only the pending migration whose floor exceeds the stable max should block")
	})

	t.Run("passes when stable has been promoted past every floor", func(t *testing.T) {
		t.Parallel()
		blocked := checkDestructiveGate(floors, 245, 251)
		require.Empty(t, blocked, "no migration should block once stable reaches every floor")
	})

	t.Run("already-applied destructive migrations never block", func(t *testing.T) {
		t.Parallel()
		blocked := checkDestructiveGate(floors, 252, 0)
		require.Empty(t, blocked, "applied migrations are enforced via schema_compat_floors, not the gate")
	})
}

func TestRunDestructiveGate_EnvHandling(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "000250_drop_legacy.up.sql",
		"-- lint:destructive-ok-after schema=\"000240\" reason=\"expand step promoted\"\nALTER TABLE issues DROP COLUMN legacy_state;\n")

	t.Run("gate off when STABLE_MAX_MIGRATION unset", func(t *testing.T) {
		t.Setenv("STABLE_MAX_MIGRATION", "")
		require.NoError(t, runDestructiveGate(dir, 249), "unset env must disable the gate for local dev and CI")
	})

	t.Run("gate blocks when stable is older than the floor", func(t *testing.T) {
		t.Setenv("STABLE_MAX_MIGRATION", "000239")
		err := runDestructiveGate(dir, 249)
		require.Error(t, err, "a pending destructive migration above the floor must block the deploy")
		require.Contains(t, err.Error(), "250", "the error should name the blocked migration")
	})

	t.Run("gate passes when stable satisfies the floor", func(t *testing.T) {
		t.Setenv("STABLE_MAX_MIGRATION", "000240")
		require.NoError(t, runDestructiveGate(dir, 249))
	})

	t.Run("non-numeric env fails closed", func(t *testing.T) {
		t.Setenv("STABLE_MAX_MIGRATION", "not-a-number")
		require.Error(t, runDestructiveGate(dir, 249), "a malformed stable version must fail the deploy, not skip the gate")
	})
}

func TestReleaseChannelsMigrationCarriesNoDestructiveAnnotation(t *testing.T) {
	t.Parallel()

	// The real migrations directory: 000245 is additive-only, so the floor
	// map must not contain it, and the repo must parse cleanly end to end.
	floors, err := parseDestructiveFloors("../../migrations")
	require.NoError(t, err, "the repository migrations directory should parse")
	_, hasFloor := floors[245]
	require.False(t, hasFloor, "000245_release_channels is additive and must not be annotated destructive")
}
