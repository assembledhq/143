package thread

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The wrappers below only observe statements on the actual supplied pgx.Tx.
// They neither answer a query nor replace its result, guard, connection, lock,
// transaction policy or settlement. A Scan barrier runs after PostgreSQL has
// returned the real row result, before that result reaches production code.
type sendPostgresAfterScanRow struct {
	pgx.Row
	after func(error) error
}

func (r sendPostgresAfterScanRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	if r.after != nil {
		return r.after(err)
	}
	return err
}

type sendPostgresAfterCloseRows struct {
	pgx.Rows
	closed bool
	after  func()
}

func (r *sendPostgresAfterCloseRows) Close() {
	r.Rows.Close()
	if !r.closed && r.Rows.Err() == nil {
		r.closed = true
		r.after()
	}
}

type sendPostgresGuardCounts struct{ shared, parent, lanes int }

type sendPostgresGuardObservedTx struct {
	pgx.Tx
	counts *sendPostgresGuardCounts
}

func sendPostgresParentLockSQL(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(query), "SELECT id, org_id, status, deleted_at, archived_at,") && strings.Contains(query, "FROM sessions") && strings.Contains(query, "FOR NO KEY UPDATE NOWAIT")
}

func (t sendPostgresGuardObservedTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := t.Tx.QueryRow(ctx, query, args...)
	shared := strings.Contains(query, "pg_try_advisory_xact_lock_shared")
	parent := sendPostgresParentLockSQL(query)
	if !shared && !parent {
		return row
	}
	return sendPostgresAfterScanRow{Row: row, after: func(err error) error {
		if err == nil {
			if shared {
				t.counts.shared++
			}
			if parent {
				t.counts.parent++
			}
		}
		return err
	}}
}

func (t sendPostgresGuardObservedTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := t.Tx.Query(ctx, query, args...)
	if err != nil || !strings.HasPrefix(strings.TrimSpace(query), "SELECT id, org_id, session_id, status, archived_at,") || !strings.Contains(query, "ORDER BY id FOR NO KEY UPDATE NOWAIT") {
		return rows, err
	}
	return &sendPostgresAfterCloseRows{Rows: rows, after: func() { t.counts.lanes++ }}, nil
}

func waitSendPostgresBarrier(ctx context.Context, ready <-chan struct{}) error {
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
