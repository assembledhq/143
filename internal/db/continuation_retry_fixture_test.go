package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Minimal job fixtures still need the relations inspected by the shared retry
// statement, even when their job type has no continuation to recover.
func addContinuationRetryFixtureSchema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS session_threads(org_id uuid, session_id uuid, id uuid);
		ALTER TABLE session_threads ADD COLUMN IF NOT EXISTS archived_at timestamptz,
			ADD COLUMN IF NOT EXISTS created_at timestamptz DEFAULT now();
		CREATE TABLE IF NOT EXISTS thread_runtimes(org_id uuid, session_id uuid);
		ALTER TABLE thread_runtimes ADD COLUMN IF NOT EXISTS id uuid,
			ADD COLUMN IF NOT EXISTS thread_id uuid, ADD COLUMN IF NOT EXISTS started_at timestamptz;
		CREATE TABLE IF NOT EXISTS session_messages(
			id bigint, org_id uuid, session_id uuid, thread_id uuid, role text, created_at timestamptz);
		CREATE TABLE IF NOT EXISTS thread_inbox_entries(
			org_id uuid, session_id uuid, thread_id uuid, message_id bigint, runtime_id uuid,
			delivery_state text, applied_at timestamptz);`)
	require.NoError(t, err, "provide empty continuation dependencies for isolated job retry fixtures")
}
