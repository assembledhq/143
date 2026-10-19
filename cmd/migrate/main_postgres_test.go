package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func newContentMigrationPostgres(t *testing.T) (*migrate.Migrate, *pgx.Conn) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL 15+ for migration upgrade proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to disposable migration test server")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, admin.Close(ctx), "close migration database administrator")
	})
	var version int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version), "read migration test server version")
	require.GreaterOrEqual(t, version, 150000, "the full schema requires PostgreSQL 15 or newer")
	name := "content_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	identifier := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+identifier)
	require.NoError(t, err, "create a fresh isolated migration database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		require.NoError(t, err, "remove only the generated migration test database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable database connection URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve the unmodified repository migration chain")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "load unique migration versions from the real repository source")
	t.Cleanup(func() {
		sourceErr, databaseErr := migration.Close()
		require.NoError(t, sourceErr, "close the migration source")
		require.NoError(t, databaseErr, "close the migration database connection")
	})
	conn, err := pgx.Connect(ctx, parsed.String())
	require.NoError(t, err, "open the generated migration database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, conn.Close(ctx), "close the isolated schema inspection connection")
	})
	return migration, conn
}

func TestSessionPRIntentContentMigrationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		startAt315 bool
	}{
		{name: "fresh migration chain"},
		{name: "upgrade preserves applied coding settings migration", startAt315: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			migration, conn := newContentMigrationPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			orgID := uuid.New()
			stamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			seedSettings := func() {
				_, err := conn.Exec(ctx, `INSERT INTO organizations(id, name) VALUES ($1, 'Migration test')`, orgID)
				require.NoError(t, err, "seed a synthetic organization in the isolated database")
				_, err = conn.Exec(ctx, `INSERT INTO coding_credential_stack_settings(org_id, distribution, created_at, updated_at) VALUES ($1, 'spread', $2, $2)`, orgID, stamp)
				require.NoError(t, err, "seed an existing coding distribution setting before the next migration")
			}
			assertVersionAndColumns := func(expectedVersion uint, expectedColumns []string) {
				version, dirty, err := migration.Version()
				require.NoError(t, err, "read the actual migration ledger")
				require.Equal(t, expectedVersion, version, "the ledger should match the selected upgrade or rollback boundary")
				require.False(t, dirty, "each successful migration boundary should remain clean")
				var columns []string
				err = conn.QueryRow(ctx, `SELECT COALESCE(array_agg(column_name::text ORDER BY column_name), ARRAY[]::text[]) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'session_pr_intents' AND column_name IN ('title', 'body')`).Scan(&columns)
				require.NoError(t, err, "inspect the staged text columns after migration")
				require.Equal(t, expectedColumns, columns, "only migration 316 should install the staged text columns")
			}
			assertSettings := func() {
				type codingSetting struct {
					OrgID        uuid.UUID
					UserID       *uuid.UUID
					Distribution string
					CreatedAt    time.Time
					UpdatedAt    time.Time
				}
				var actual codingSetting
				err := conn.QueryRow(ctx, `SELECT org_id, user_id, distribution, created_at, updated_at FROM coding_credential_stack_settings WHERE org_id = $1 AND user_id IS NULL`, orgID).Scan(&actual.OrgID, &actual.UserID, &actual.Distribution, &actual.CreatedAt, &actual.UpdatedAt)
				require.NoError(t, err, "read the existing tenant's coding distribution setting")
				actual.CreatedAt, actual.UpdatedAt = actual.CreatedAt.UTC(), actual.UpdatedAt.UTC()
				expected := codingSetting{OrgID: orgID, UserID: nil, Distribution: "spread", CreatedAt: stamp, UpdatedAt: stamp}
				require.Equal(t, expected, actual, "upgrade and rollback must preserve every existing coding setting field")
			}
			if tt.startAt315 {
				require.NoError(t, migration.Migrate(315), "apply the canonical coding settings migration before upgrading")
				assertVersionAndColumns(315, []string{})
				seedSettings()
			}
			// Pin this historical boundary so adding later migrations does not
			// change the upgrade/rollback contract this regression exercises.
			require.NoError(t, migration.Migrate(316), "apply the full real migration chain through staged PR content")
			assertVersionAndColumns(316, []string{"body", "title"})
			if !tt.startAt315 {
				seedSettings()
			}
			assertSettings()
			var constraints []string
			err := conn.QueryRow(ctx, `SELECT COALESCE(array_agg(conname::text ORDER BY conname), ARRAY[]::text[]) FROM pg_constraint WHERE conrelid = 'public.session_pr_intents'::regclass AND conname IN ('session_pr_intents_body_shape', 'session_pr_intents_title_shape') AND convalidated`).Scan(&constraints)
			require.NoError(t, err, "inspect both validated staged-content constraints")
			require.Equal(t, []string{"session_pr_intents_body_shape", "session_pr_intents_title_shape"}, constraints, "the renumbered migration should retain both content constraints")
			require.NoError(t, migration.Steps(-1), "rollback only the staged PR content migration")
			assertVersionAndColumns(315, []string{})
			assertSettings()
			require.NoError(t, migration.Migrate(316), "reapply staged content after its rollback")
			assertVersionAndColumns(316, []string{"body", "title"})
			assertSettings()
		})
	}
}
