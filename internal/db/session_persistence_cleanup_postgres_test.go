package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionPersistenceCleanupRejectsOwnedConnectionPostgres(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for ordinary fixture-drop proofs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, raw)
	require.True(t, err == nil, "connect only to the supplied disposable server without printing its DSN")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close the isolated cleanup proof's administrator: %v", err)
		}
	})
	name := "session_cleanup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err, "create only the randomly generated cleanup proof database")
	dropped := false
	t.Cleanup(func() {
		if !dropped {
			dropSessionPersistencePostgresDatabase(t, admin, name)
		}
	})
	configuration := admin.Config().Copy()
	configuration.Database = name
	owned, err := pgx.ConnectConfig(ctx, configuration)
	require.True(t, err == nil, "open exactly one connection against only the generated database")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := owned.Close(ctx); err != nil {
			t.Errorf("close the cleanup proof's owned database connection: %v", err)
		}
	})

	reporter := &sessionPersistenceCleanupRecorder{}
	dropSessionPersistencePostgresDatabase(reporter, admin, name)
	require.Len(t, reporter.errors, 1, "ordinary DROP must fail visibly while this proof still owns a live database connection")
	require.Contains(t, reporter.errors[0], "SQLSTATE 55006", "the failed drop must expose PostgreSQL's object-in-use error rather than conceal the connection leak")
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelProbe()
	var connectedDatabase string
	require.NoError(t, owned.QueryRow(probeCtx, "SELECT current_database()").Scan(&connectedDatabase), "ordinary cleanup must not terminate the deliberately retained connection")
	require.Equal(t, name, connectedDatabase, "the retained connection must still query exactly the generated database")
	require.NoError(t, owned.Close(probeCtx), "release this proof's only active fixture connection before dropping normally")
	reporter = &sessionPersistenceCleanupRecorder{}
	dropSessionPersistencePostgresDatabase(reporter, admin, name)
	require.Equal(t, []string(nil), reporter.errors, "the same ordinary drop must succeed once its only owned connection closes")
	dropped = true
	verifyCtx, cancelVerify := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelVerify()
	var exists bool
	require.NoError(t, admin.QueryRow(verifyCtx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists), "verify cleanup against only the randomly generated database's catalog entry")
	require.False(t, exists, "normal cleanup must remove the generated fixture after owned connections close")
}
