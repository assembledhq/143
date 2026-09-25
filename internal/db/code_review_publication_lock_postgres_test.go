package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewPublicationLockBoundsServerTransaction(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL publication timeout proof")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	t.Cleanup(pool.Close)
	err = NewCodeReviewStore(pool).RunWithGitHubPublicationLock(ctx, uuid.New(), uuid.New(), func(lockCtx context.Context, tx DBTX) error {
		for _, tt := range []struct{ setting, expected string }{
			{setting: "lock_timeout", expected: "15s"},
			{setting: "statement_timeout", expected: "2min"},
			{setting: "idle_in_transaction_session_timeout", expected: "6min"},
		} {
			var actual string
			if err := tx.QueryRow(lockCtx, `SELECT current_setting($1)`, tt.setting).Scan(&actual); err != nil {
				return err
			}
			require.Equal(t, tt.expected, actual, "publication transaction should enforce the expected server-side timeout")
		}
		deadline, ok := lockCtx.Deadline()
		require.True(t, ok, "publication work should have a deadline after lock acquisition")
		require.LessOrEqual(t, time.Until(deadline), codeReviewPublicationLockTimeout, "publication work should fit within its deadline")
		return nil
	})
	require.NoError(t, err, "bounded publication transaction should commit")
}
