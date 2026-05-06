package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// TestEnsureAnthropicSplitSentinel covers the three branches of the startup
// gate:
//
//   - sentinel row exists → no-op, returns nil.
//   - sentinel absent + zero anthropic rows → auto-write sentinel, returns nil.
//   - sentinel absent + anthropic rows present → returns
//     ErrAnthropicSplitSentinelMissing without writing.
//
// Plus the I/O error paths so a transient pg error surfaces a wrapped error
// instead of a silent pass.
func TestEnsureAnthropicSplitSentinel(t *testing.T) {
	t.Parallel()

	t.Run("sentinel present passes immediately", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnRows(pgxmock.NewRows([]string{"name"}).AddRow(AnthropicSplitSentinel))

		require.NoError(t, EnsureAnthropicSplitSentinel(context.Background(), mock))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("fresh install auto-writes sentinel when no anthropic rows", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(`INSERT INTO coding_credentials_migrations`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))

		require.NoError(t, EnsureAnthropicSplitSentinel(context.Background(), mock))
		require.NoError(t, mock.ExpectationsWereMet(),
			"fresh install must auto-write the sentinel so the gate does not block on empty databases")
	})

	t.Run("anthropic rows present without sentinel returns ErrAnthropicSplitSentinelMissing", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(7))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.ErrorIs(t, err, ErrAnthropicSplitSentinelMissing,
			"databases with pre-split rows must surface the operator-actionable error")
		require.NoError(t, mock.ExpectationsWereMet(),
			"gate must not write the sentinel when split work is still pending")
	})

	t.Run("legacy org_credentials rows without unified rows still block the gate", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Simulates the partial-migration state where 000110 has applied but
		// 000111 has not (or failed mid-flight): coding_credentials is empty
		// but legacy tables still hold pre-split anthropic rows.
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.ErrorIs(t, err, ErrAnthropicSplitSentinelMissing,
			"partial migration with legacy anthropic rows must not auto-pass the gate")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("legacy user_credentials rows without unified rows still block the gate", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.ErrorIs(t, err, ErrAnthropicSplitSentinelMissing,
			"legacy user_credentials anthropic rows must keep the gate closed")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("sentinel lookup error is wrapped", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		boom := errors.New("connection reset by peer")
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(boom)

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.Error(t, err, "I/O error on sentinel lookup must surface")
		require.ErrorIs(t, err, boom, "wrapped error must preserve the underlying cause")
	})

	t.Run("count error after missing sentinel is wrapped", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		boom := errors.New("relation \"coding_credentials\" does not exist")
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnError(boom)

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.Error(t, err)
		require.ErrorIs(t, err, boom, "count failure must surface as a wrapped error")
	})

	t.Run("insert failure on auto-write surfaces", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		boom := errors.New("permission denied for table coding_credentials_migrations")
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectExec(`INSERT INTO coding_credentials_migrations`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(boom)

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.Error(t, err)
		require.ErrorIs(t, err, boom, "auto-write failure must surface as a wrapped error")
	})

	t.Run("legacy org count error is wrapped", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		boom := errors.New("relation \"org_credentials\" does not exist")
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnError(boom)

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.Error(t, err)
		require.ErrorIs(t, err, boom, "legacy org count failure must surface as a wrapped error")
	})

	t.Run("legacy user count error is wrapped", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		boom := errors.New("relation \"user_credentials\" does not exist")
		mock.ExpectQuery(`SELECT name FROM coding_credentials_migrations WHERE name = \$1`).
			WithArgs(AnthropicSplitSentinel).
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectQuery(`SELECT count\(\*\) FROM "coding_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "org_credentials" WHERE provider = 'anthropic'`).
			WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectQuery(`SELECT count\(\*\) FROM "user_credentials" WHERE provider = 'anthropic'`).
			WillReturnError(boom)

		err = EnsureAnthropicSplitSentinel(context.Background(), mock)
		require.Error(t, err)
		require.ErrorIs(t, err, boom, "legacy user count failure must surface as a wrapped error")
	})
}
