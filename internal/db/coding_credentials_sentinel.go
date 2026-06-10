package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AnthropicSplitSentinel is the sentinel name written to
// coding_credentials_migrations by the migrate-coding-credentials-anthropic-split
// command on completion. The server's startup gate refuses to serve traffic
// until this row exists (or a fresh-install fallback determines no split work
// is needed).
const AnthropicSplitSentinel = "anthropic_split"

// ErrAnthropicSplitSentinelMissing indicates the unified-credentials post-step
// has not run on this database. Operators should run
// `make migrate-coding-credentials-anthropic-split` before the server boots.
var ErrAnthropicSplitSentinelMissing = errors.New(
	"anthropic_split sentinel missing — run `make migrate-coding-credentials-anthropic-split` before serving",
)

// EnsureAnthropicSplitSentinel verifies the post-step migration has run, or
// auto-marks completion when there is provably nothing to split. The check
// covers both the unified table and the legacy tables, because a partial
// migration (000110 applied, 000111 not yet) leaves coding_credentials empty
// while pre-split rows still live in org_credentials/user_credentials. Without
// the legacy check, that mid-migration state would auto-pass the gate and the
// post-step would never execute on a later boot.
//
// Returns ErrAnthropicSplitSentinelMissing when the sentinel is absent and
// any anthropic row exists in the unified or legacy tables; returns wrapped
// errors for I/O failures.
//
// lint:allow-no-orgid reason="schema-level invariant; not tenant data"
func EnsureAnthropicSplitSentinel(ctx context.Context, dbtx DBTX) error {
	present, err := anthropicSplitSentinelPresent(ctx, dbtx)
	if err != nil {
		return fmt.Errorf("check anthropic_split sentinel: %w", err)
	}
	if present {
		return nil
	}

	// Sentinel absent. Auto-write only when no anthropic rows exist anywhere
	// — unified or legacy. Legacy rows that haven't yet been copied by 000111
	// still represent split work the post-step must do once the data lands in
	// coding_credentials.
	unifiedCount, err := countActiveUnifiedAnthropicRows(ctx, dbtx)
	if err != nil {
		return fmt.Errorf("count pre-split anthropic rows: %w", err)
	}
	legacyOrgCount, err := countAnthropicRows(ctx, dbtx, "org_credentials")
	if err != nil {
		return fmt.Errorf("count legacy org anthropic rows: %w", err)
	}
	legacyUserCount, err := countAnthropicRows(ctx, dbtx, "user_credentials")
	if err != nil {
		return fmt.Errorf("count legacy user anthropic rows: %w", err)
	}
	if unifiedCount > 0 || legacyOrgCount > 0 || legacyUserCount > 0 {
		return ErrAnthropicSplitSentinelMissing
	}

	if _, err := dbtx.Exec(ctx,
		`INSERT INTO coding_credentials_migrations (name) VALUES ($1)
		 ON CONFLICT (name) DO NOTHING`,
		AnthropicSplitSentinel,
	); err != nil {
		return fmt.Errorf("auto-write anthropic_split sentinel: %w", err)
	}
	return nil
}

// countAnthropicRows returns the number of provider='anthropic' rows in the
// named table. Table name is sanitized via pgx.Identifier rather than passed
// as a parameter because the SQL parser doesn't accept a placeholder in that
// position; the call sites pass only fixed string literals, but the explicit
// quoting makes the intent visible to the next reader.
func countAnthropicRows(ctx context.Context, dbtx DBTX, table string) (int, error) {
	var count int
	q := `SELECT count(*) FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE provider = 'anthropic'`
	if err := dbtx.QueryRow(ctx, q).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// countActiveUnifiedAnthropicRows is the coding_credentials variant of
// countAnthropicRows. Under the insert-only versioning schema (migration
// 000165) inactive rows are immutable history and never deleted, so an
// anthropic credential that was deactivated long ago must not hold the boot
// gate closed — only active config versions represent split work.
//
// lint:allow-no-orgid reason="schema-level invariant; not tenant data"
func countActiveUnifiedAnthropicRows(ctx context.Context, dbtx DBTX) (int, error) {
	var count int
	q := `SELECT count(*) FROM coding_credentials WHERE provider = 'anthropic' AND active = true`
	if err := dbtx.QueryRow(ctx, q).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func anthropicSplitSentinelPresent(ctx context.Context, dbtx DBTX) (bool, error) {
	var name string
	err := dbtx.QueryRow(ctx,
		`SELECT name FROM coding_credentials_migrations WHERE name = $1`,
		AnthropicSplitSentinel,
	).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
