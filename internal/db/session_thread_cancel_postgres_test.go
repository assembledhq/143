package db

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestMarkCancelRequestedForTurnPostgres(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for atomic cancellation fence proof")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable PostgreSQL")
	t.Cleanup(func() { require.NoError(t, conn.Close(ctx), "close cancellation fixture connection") })
	_, err = conn.Exec(ctx, `CREATE TEMP TABLE session_threads(org_id uuid,session_id uuid,id uuid,current_turn int,status text,archived_at timestamptz,cancel_requested_at timestamptz)`)
	require.NoError(t, err, "create isolated cancellation fixture")
	org, session, thread := uuid.New(), uuid.New(), uuid.New()
	_, err = conn.Exec(ctx, `INSERT INTO session_threads(org_id,session_id,id,current_turn,status) VALUES($1,$2,$3,0,'running')`, org, session, thread)
	require.NoError(t, err, "start original thread turn")
	store := NewSessionThreadStore(conn)
	marked, err := store.MarkCancelRequestedForTurn(ctx, org, session, thread, 1)
	require.NoError(t, err, "mark original turn cancellation atomically")
	require.True(t, marked, "matching turn should receive cancellation")
	_, err = conn.Exec(ctx, `UPDATE session_threads SET current_turn=1,cancel_requested_at=NULL WHERE org_id=$1 AND id=$2`, org, thread)
	require.NoError(t, err, "represent old turn drained and next turn running")
	marked, err = store.MarkCancelRequestedForTurn(ctx, org, session, thread, 1)
	require.NoError(t, err, "delayed old cancellation should be a harmless no-op")
	require.False(t, marked, "delayed cancellation must not mark the new turn")
	var untouched bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT cancel_requested_at IS NULL FROM session_threads WHERE org_id=$1 AND id=$2`, org, thread).Scan(&untouched), "read new turn cancellation marker")
	require.True(t, untouched, "new run must not inherit the old cancellation timestamp")
	marked, err = store.MarkCancelRequestedForTurn(ctx, uuid.New(), session, thread, 2)
	require.NoError(t, err, "another tenant cancellation should be a no-op")
	require.False(t, marked, "cancellation must filter by organization")
}
