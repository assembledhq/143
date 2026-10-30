package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// The original 384–389 regression suites intentionally exercise those frozen
// boundaries, including raw SQL downs. Remove the appended prerequisite first
// in correct order rather than silently running an old down against a latest
// schema. This is a raw-SQL fixture pin, not migration-ledger evidence. Separate
// appended-schema and actual driver/CLI tests cover the new highest boundary.
func callerDispatchStatusPinOriginal389(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, version := range []int{392, 391, 390} {
		_, err := pool.Exec(ctx, callerDispatchStatusMigrationSQL(t, version, "down"))
		require.NoError(t, err, "pin the historical disposable fixture by traversing each appended down first")
	}
	var oldValidated, newAbsent bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_check' AND convalidated),
NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='code_review_session_metadata'::regclass AND conname='code_review_session_metadata_status_code_v2_check')`).Scan(&oldValidated, &newAbsent), "independently verify the historical fixture's actual restored constraint identity")
	require.Equal(t, [2]bool{true, true}, [2]bool{oldValidated, newAbsent}, "historical tests must run on the exact validated old allowance, not merely an older label")
}
