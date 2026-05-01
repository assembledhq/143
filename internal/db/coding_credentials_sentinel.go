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
// auto-marks completion when there is provably nothing to split (no
// provider='anthropic' rows in coding_credentials). Fresh installs pass
// without operator action; databases that contain pre-split anthropic rows
// must run the post-step before this returns nil.
//
// Returns ErrAnthropicSplitSentinelMissing when the sentinel is absent and
// pre-split rows exist; returns wrapped errors for I/O failures.
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

	// Sentinel absent. If there are no anthropic rows at all, the post-step
	// has nothing to do — auto-write the sentinel so fresh installs and
	// freshly-truncated test databases boot cleanly.
	var count int
	if err := dbtx.QueryRow(ctx,
		`SELECT count(*) FROM coding_credentials WHERE provider = 'anthropic'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("count pre-split anthropic rows: %w", err)
	}
	if count > 0 {
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
