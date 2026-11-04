package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	os.Exit(testpostgres.Run(m))
}

// newMigratedSchemaPostgres is for behavior that needs the latest schema, not
// proofs of applying, upgrading, or rolling back the repository migrations.
// Each test gets its own clone of the template migrated once by this process.
func newMigratedSchemaPostgres(t *testing.T) (*migrate.Migrate, *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 1)
	name := pool.Config().ConnConfig.Database
	// Census assertions observe real PostgreSQL connections. Retain the same
	// raw actor/migrator topology without an extra idle pool connection.
	pool.Close()
	// ConnString retains pgx's originally parsed URL even when its Database
	// field changes. Reparse an explicit clone URL so subprocess probes also
	// target the disposable clone, including with query-string overrides.
	isolatedURL, err := contentMigrationDatabaseURL(os.Getenv("TEST_DATABASE_URL"), name)
	require.NoError(t, err, "select only the migrated clone for both connection drivers and child commands")
	actor, err := pgx.Connect(ctx, isolatedURL)
	require.NoError(t, err, "open a raw inspection connection to the migrated clone")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, actor.Close(ctx), "close the migrated clone inspection connection before database cleanup")
	})
	var actualDatabase string
	require.NoError(t, actor.QueryRow(ctx, "SELECT current_database()").Scan(&actualDatabase), "verify the migrated fixture connection before running any behavior probe")
	require.Equal(t, name, actualDatabase, "inspection must target only the generated migrated clone")
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve the unmodified repository migration source for ledger inspection")
	migration, err := migrate.New("file://"+source, isolatedURL)
	require.NoError(t, err, "open the independent migration ledger connection without replaying the schema")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close the migrated fixture source")
		require.NoError(t, databaseErr, "close the migrated fixture ledger connection before database cleanup")
	})
	_, dirty, err := migration.Version()
	require.NoError(t, err, "the clone must inherit an applied repository migration ledger")
	require.False(t, dirty, "the migrated clone must start with a clean schema ledger")
	return migration, actor
}

func TestMigratedSchemaPostgresFixturesIsolateState(t *testing.T) {
	t.Parallel()
	firstMigration, first := newMigratedSchemaPostgres(t)
	require.ErrorIs(t, firstMigration.Up(), migrate.ErrNoChange, "a migrated clone must already include every repository migration")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := first.Exec(ctx, `INSERT INTO lifecycle_writer_instances(instance_id,role,build_sha,last_heartbeat,expires_at) VALUES ($1,'api','synthetic-fixture',statement_timestamp(),statement_timestamp()+interval '1 minute')`, uuid.New())
	require.NoError(t, err, "seed only the first clone before creating another fixture")
	_, second := newMigratedSchemaPostgres(t)
	require.NotEqual(t, first.Config().Database, second.Config().Database, "each behavior fixture must have its own disposable database")
	var firstRows, secondRows int
	require.NoError(t, first.QueryRow(ctx, `SELECT count(*) FROM lifecycle_writer_instances`).Scan(&firstRows), "read the original clone after creating the next fixture")
	require.NoError(t, second.QueryRow(ctx, `SELECT count(*) FROM lifecycle_writer_instances`).Scan(&secondRows), "read the next clone independently of the first fixture's mutation")
	require.Equal(t, []int{1, 0}, []int{firstRows, secondRows}, "mutations must stay in their owning clone and never reach the shared template")
	parsed, err := pgx.ParseConfig(second.Config().ConnString())
	require.NoError(t, err, "parse the exact URL that subprocess probes derive from the actor")
	require.Equal(t, second.Config().Database, parsed.Database, "the actor's original connection string must identify its clone, not the template or input database")
	var connections int
	require.NoError(t, second.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND backend_type='client backend'`).Scan(&connections), "observe every retained client connection in the isolated fixture")
	require.Equal(t, 2, connections, "census fixtures must retain only the raw actor and migration ledger connections")
}
